package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// The planned resource names on the task are diagnostic only. This record
// belongs to one restore attempt and is updated after actual creation.
type portableCleanupProof struct {
	Attempt string            `json:"attempt"`
	Sealed  bool              `json:"sealed,omitempty"`
	Owned   map[string]string `json:"owned"`
}

func (s *PortableImports) saveCleanupProof(id string, proof *portableCleanupProof) error {
	raw, err := json.Marshal(proof)
	if err != nil {
		return err
	}
	result, err := s.db.Exec("UPDATE portable_import_tasks SET cleanup_proof=? WHERE id=? AND status='running'", string(raw), id)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrPortableState
	}
	return nil
}

func (s *PortableImports) recordPortablePaths(id string, proof *portableCleanupProof, paths ...string) error {
	for _, path := range paths {
		identity, err := portablePathIdentity(path)
		if err != nil {
			return err
		}
		proof.Owned[path] = identity
	}
	return s.saveCleanupProof(id, proof)
}

// Birth time distinguishes a replaced path even when the filesystem reuses
// the old inode. Filesystems without it cannot certify deletion.
func portablePathIdentity(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("站点路径无效")
	}
	var st unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_BTIME, &st); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return "", nil
		}
		return "", err
	}
	parent := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	if resolved != parent {
		return "unverified", nil
	}
	if st.Mask&unix.STATX_BTIME == 0 {
		return "unverified", nil
	}
	identity := fmt.Sprintf("%d:%d:%d:%d:%d:%d", st.Dev_major, st.Dev_minor, st.Ino, st.Btime.Sec, st.Btime.Nsec, st.Mode&unix.S_IFMT)
	if st.Mode&unix.S_IFMT == unix.S_IFREG {
		// The only regular resources are small root-owned service configs.
		if st.Uid != 0 || st.Mode&0022 != 0 || st.Size > 1<<20 {
			return "unverified", nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		identity += fmt.Sprintf(":%x", sha256.Sum256(content))
	}
	return identity, nil
}

func portableAccountEntry(kind, name string, fields int) ([]string, error) {
	output, err := exec.Command("getent", kind, name).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 2 {
			return nil, nil
		}
		return nil, err
	}
	entry := strings.Split(strings.TrimSuffix(string(output), "\n"), ":")
	if len(entry) != fields || entry[0] != name {
		return nil, errors.New("无法核对系统账号或用户组")
	}
	return entry, nil
}

func portableAccountIdentity(name string) (string, error) {
	entry, err := portableAccountEntry("passwd", name, 7)
	if err != nil || entry == nil {
		return "", err
	}
	return strings.Join([]string{entry[4], entry[2], entry[3], entry[5], entry[6]}, ":"), nil
}

func portableGroupIdentity(name string) (string, error) {
	entry, err := portableAccountEntry("group", name, 4)
	if err != nil || entry == nil {
		return "", err
	}
	return entry[2] + ":" + entry[0] + ":" + entry[3], nil
}

func (s *PortableImports) portableMySQLQuery(query string) (string, error) {
	cfg := s.cfg.MariaDB
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "mysql", "-u", cfg.RootUser, "-N", "-B", "-r", "-e", query)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.RootPassword)
	result, err := cmd.Output()
	return strings.TrimRight(string(result), "\n"), err
}

func portableDatabaseIdentity(query func(string) (string, error), res siteProvisioningResources) (string, string, error) {
	out, err := query(fmt.Sprintf("SELECT CONCAT('db:',HEX(SCHEMA_COMMENT)) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='%s'; SELECT CONCAT('account:',HEX(Host)) FROM mysql.user WHERE User='%s' ORDER BY Host;", res.DBName, res.DBUser))
	if err != nil {
		return "", "", err
	}
	var db, account string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "db:") && db == "":
			comment, err := hex.DecodeString(strings.TrimPrefix(line, "db:"))
			if err != nil {
				return "", "", err
			}
			db = "db:" + string(comment)
		case line == "account:6C6F63616C686F7374" && account == "":
			account = "localhost"
		case strings.HasPrefix(line, "account:"):
			account = "unverified"
		case line != "":
			return "", "", errors.New("无法读取目标数据库归属")
		}
	}
	if account == "localhost" {
		grants, err := query(fmt.Sprintf("SHOW GRANTS FOR '%s'@'localhost'", res.DBUser))
		if err != nil || grants == "" {
			return "", "", errors.New("无法读取目标数据库账号归属")
		}
		account = fmt.Sprintf("%x", sha256.Sum256([]byte(grants)))
	}
	return db, account, nil
}

type PortableRetryPlan struct {
	Resources      []string `json:"resources,omitempty"`
	MaterialsReady bool     `json:"materials_ready"`
	Token          string   `json:"token,omitempty"`
	owned          map[string]string
	res            siteProvisioningResources
}

func (s *PortableImports) RetryPlan(id string, aliases []string) (PortableRetryPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.task(id)
	if err != nil {
		return PortableRetryPlan{}, err
	}
	if s.runningID != "" || (task.Status != "failed" && task.Status != "interrupted" && task.Phase != portableRetryUploadPhase) {
		return PortableRetryPlan{}, ErrPortableState
	}
	if aliases != nil {
		task.Aliases, err = normalizeSiteProvisioningAliases(aliases, task.Domain)
		if err != nil {
			return PortableRetryPlan{}, err
		}
	}
	plan, err := s.portableRetryPlan(task)
	if err != nil {
		return PortableRetryPlan{}, err
	}
	dir, err := s.taskDir(id)
	if err == nil && task.ExpiresAt > time.Now().Unix() {
		_, _, files, readErr := s.loadFiles(id, dir)
		if readErr == nil {
			plan.MaterialsReady = true
			for _, file := range files {
				if size, err := portableFileSize(filepath.Join(dir, file.Name)); err != nil || size != file.Size {
					plan.MaterialsReady = false
					break
				}
			}
		}
	}
	return plan, nil
}

func (s *PortableImports) portableRetryPlan(task PortableTask) (PortableRetryPlan, error) {
	if task.SiteID != 0 {
		return PortableRetryPlan{}, errors.New("任务已关联网站，不能清理站点资源")
	}
	if err := checkPortableDomainAndMigration(context.Background(), task.Domain, task.Aliases); err != nil {
		return PortableRetryPlan{}, err
	}
	res, err := deriveProvisioningResources(s.cfg, task.Domain, "wordpress", buildSiteName(task.Domain))
	if err != nil {
		return PortableRetryPlan{}, err
	}
	var claimed int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM websites WHERE system_user=? OR web_root=? OR log_dir=? OR db_name=? OR db_user=? OR nginx_conf_path=? OR php_pool_path=?`,
		res.SystemUser, res.WebRoot, res.LogDir, res.DBName, res.DBUser, res.NginxConfigPath, res.PHPFPMPath).Scan(&claimed); err != nil || claimed != 0 {
		return PortableRetryPlan{}, errors.New("目标资源已被网站占用或无法核对")
	}
	var raw string
	if err := s.db.QueryRow("SELECT cleanup_proof FROM portable_import_tasks WHERE id=?", task.ID).Scan(&raw); err != nil {
		return PortableRetryPlan{}, err
	}
	var proof portableCleanupProof
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &proof); err != nil || !portableID.MatchString(proof.Attempt) {
			return PortableRetryPlan{}, errors.New("无法核对任务的残留资源记录")
		}
	}
	plan := PortableRetryPlan{res: res, owned: make(map[string]string)}
	var conflicts []string
	consider := func(key, label, found, expected string) {
		if found == "" {
			return
		}
		if proof.Sealed || found == "unverified" || expected != found {
			conflicts = append(conflicts, label)
			return
		}
		plan.Resources = append(plan.Resources, label)
		plan.owned[key] = found
	}
	for _, path := range portableRetryPaths(s, task.Domain, res) {
		found, err := portablePathIdentity(path)
		if err != nil {
			return PortableRetryPlan{}, fmt.Errorf("无法核对站点资源 %s：%w", path, err)
		}
		consider(path, path, found, proof.Owned[path])
	}
	account, err := portableAccountIdentity(res.SystemUser)
	if err != nil {
		return PortableRetryPlan{}, err
	}
	consider("user", "系统账号 "+res.SystemUser, account, proof.Owned["user"])
	group, err := portableGroupIdentity(res.SystemUser)
	if err != nil {
		return PortableRetryPlan{}, err
	}
	consider("group", "系统用户组 "+res.SystemUser, group, proof.Owned["group"])
	query := s.portableMySQLQuery
	if s.retryDBQuery != nil {
		query = s.retryDBQuery
	}
	db, dbUser, err := portableDatabaseIdentity(query, res)
	if err != nil {
		return PortableRetryPlan{}, errors.New("暂时无法核对目标数据库和数据库账号")
	}
	dbProof := ""
	if proof.Attempt != "" {
		dbProof = "db:" + portableOwnerLabel(proof.Attempt)
	}
	consider("database", "数据库 "+res.DBName, db, dbProof)
	consider("database-user", "数据库账号 "+res.DBUser, dbUser, proof.Owned["database-user"])
	if len(conflicts) > 0 {
		return PortableRetryPlan{}, fmt.Errorf("以下资源已存在，无法确认由本任务创建且未发布，请由服务器管理员核对：%s", strings.Join(conflicts, "、"))
	}
	if len(plan.owned) > 0 {
		key, err := json.Marshal([]any{task.ID, task.Aliases, proof.Attempt, plan.owned})
		if err != nil {
			return PortableRetryPlan{}, err
		}
		plan.Token = fmt.Sprintf("%x", sha256.Sum256(key))
	}
	return plan, nil
}

func portableOwnerLabel(attempt string) string { return "wp-panel-portable-" + attempt }

func portableRetryPaths(s *PortableImports, domain string, res siteProvisioningResources) []string {
	return []string{res.WebRoot, res.LogDir, sitePluginSecretsDir(domain), filepath.Join(s.cfg.Paths.Certificates, domain),
		res.NginxConfigPath, res.NginxEnabledPath, res.PHPFPMPath, res.PHPSocketPath,
		filepath.Join(nginxCustomDir, domain+".conf"), filepath.Join(nginxCustomDir, domain+".pre.conf")}
}

func (s *PortableImports) cleanPortableRetry(task PortableTask, token string) error {
	maintenance, err := acquireMaintenanceLifecycleFile(s.cfg)
	if err != nil {
		return ErrPortableBusy
	}
	defer maintenance.Close()
	creation, err := acquireSiteCreationLock()
	if err != nil {
		return ErrPortableBusy
	}
	defer creation.Close()
	plan, err := s.portableRetryPlan(task)
	if err != nil {
		return err
	}
	if token != plan.Token {
		return errors.New("残留资源已变化，请重新检查并确认")
	}
	if token == "" {
		return nil
	}
	if err := s.phase(task.ID, "running", "cleanup", ""); err != nil {
		return err
	}
	res := plan.res
	directories := []string{res.WebRoot, res.LogDir, sitePluginSecretsDir(task.Domain), filepath.Join(s.cfg.Paths.Certificates, task.Domain)}
	if err := checkPortableCleanupMounts(plan, directories); err != nil {
		return err
	}
	if err := checkPortableGroupMembers(plan); err != nil {
		return err
	}

	remove := func(path string) error {
		if plan.owned[path] == "" {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("清理 %s：%w", path, err)
		}
		return nil
	}
	if err := remove(res.NginxEnabledPath); err != nil {
		return err
	}
	if plan.owned[res.NginxConfigPath] != "" || plan.owned[res.NginxEnabledPath] != "" {
		if _, err := executeCommand("nginx", "-t"); err != nil {
			return fmt.Errorf("清理维护配置后的 Nginx 检查失败：%w", err)
		}
		if _, err := executeCommand("nginx", "-s", "reload"); err != nil {
			return fmt.Errorf("清理维护配置后的 Nginx 重载失败：%w", err)
		}
		if err := remove(res.NginxConfigPath); err != nil {
			return err
		}
	}
	if err := remove(res.PHPFPMPath); err != nil {
		return err
	}
	if plan.owned[res.PHPFPMPath] != "" || plan.owned[res.PHPSocketPath] != "" {
		if err := runPHPFPMServiceAction("reload"); err != nil {
			return err
		}
		for attempt := 0; ; attempt++ {
			_, err := os.Lstat(res.PHPSocketPath)
			if os.IsNotExist(err) {
				break
			}
			if err != nil || attempt >= 149 {
				return errors.New("未确认失败站点的 PHP-FPM 套接字已退出")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	dbCfg := s.cfg.MariaDB
	if plan.owned["database"] != "" {
		if err := runMySQL(dbCfg.RootPassword, "-u", dbCfg.RootUser, "-e", fmt.Sprintf("DROP DATABASE `%s`", res.DBName)); err != nil {
			return fmt.Errorf("清理数据库 %s：%w", res.DBName, err)
		}
	}
	if plan.owned["database-user"] != "" {
		if err := runMySQL(dbCfg.RootPassword, "-u", dbCfg.RootUser, "-e", fmt.Sprintf("DROP USER '%s'@'localhost'", res.DBUser)); err != nil {
			return fmt.Errorf("清理数据库账号 %s：%w", res.DBUser, err)
		}
	}
	for _, path := range directories {
		if plan.owned[path] != "" {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("清理目录 %s：%w", path, err)
			}
		}
	}
	if plan.owned["user"] != "" {
		if _, err := executeCommand("userdel", res.SystemUser); err != nil {
			return fmt.Errorf("清理系统账号 %s：%w", res.SystemUser, err)
		}
	}
	if plan.owned["group"] != "" {
		current, err := portableGroupIdentity(res.SystemUser)
		if err != nil || (current != "" && current != plan.owned["group"]) {
			return errors.New("无法核对剩余系统用户组")
		}
		if current != "" {
			if _, err := executeCommand("groupdel", res.SystemUser); err != nil {
				return fmt.Errorf("清理系统用户组 %s：%w", res.SystemUser, err)
			}
		}
	}
	remaining, err := s.portableRetryPlan(task)
	if err != nil {
		return err
	}
	if len(remaining.Resources) != 0 {
		return fmt.Errorf("仍有资源需要清理：%s", strings.Join(remaining.Resources, "、"))
	}
	return s.phase(task.ID, "running", "validating", "")
}

func checkPortableCleanupMounts(plan PortableRetryPlan, directories []string) error {
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("无法核对站点目录的挂载状态：%w", err)
	}
	decode := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mount := decode.Replace(fields[4])
		for _, dir := range directories {
			if plan.owned[dir] != "" && pathWithin(dir, mount, true) {
				return fmt.Errorf("残留目录存在挂载点，请由管理员核对：%s", mount)
			}
		}
	}
	return nil
}

func checkPortableGroupMembers(plan PortableRetryPlan) error {
	group := plan.owned["group"]
	if group == "" {
		return nil
	}
	gid, entry, _ := strings.Cut(group, ":")
	if !strings.HasSuffix(entry, ":") {
		return errors.New("站点用户组包含其他成员，请由管理员核对")
	}
	passwd, err := exec.Command("getent", "passwd").Output()
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(passwd), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 3 && fields[3] == gid && fields[0] != plan.res.SystemUser {
			return errors.New("站点用户组已被其他账号使用，请由管理员核对")
		}
	}
	return nil
}
