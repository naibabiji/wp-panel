package executor

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const wpCoreUpdatePHPSource = `
$wp_panel_action=$argv[1]??'';$wp_panel_root=$argv[2]??'';$wp_panel_package=$argv[3]??'';$wp_panel_target=$argv[4]??'';$wp_panel_expected=$argv[5]??'';$wp_panel_token=getenv('WP_PANEL_RUNNER_TOKEN');$wp_panel_sent=false;
$wp_panel_send=function($ok,$version='',$code='')use(&$wp_panel_sent,$wp_panel_token){if($wp_panel_sent)return;$wp_panel_sent=true;$body=json_encode(['token'=>$wp_panel_token,'ok'=>$ok,'version'=>$version,'error_code'=>$code],JSON_UNESCAPED_SLASHES);file_put_contents('php://fd/3',$body);};
register_shutdown_function(function()use(&$wp_panel_sent,$wp_panel_send){if(!$wp_panel_sent){$e=error_get_last();$wp_panel_send(false,'',$e?'fatal_error':'no_result');}});
if(PHP_SAPI!=='cli'||!preg_match('/^[0-9a-f]{32}$/',$wp_panel_token)||!is_dir($wp_panel_root)||realpath($wp_panel_root)!==rtrim($wp_panel_root,'/')){$wp_panel_send(false,'','invalid_input');exit(2);}chdir($wp_panel_root);ob_start();
if(!defined('WP_USE_THEMES'))define('WP_USE_THEMES',false);if(!defined('WP_INSTALLING'))define('WP_INSTALLING',true);if(!defined('CORE_UPGRADE_SKIP_NEW_BUNDLED'))define('CORE_UPGRADE_SKIP_NEW_BUNDLED',true);if(!defined('FS_METHOD'))define('FS_METHOD','direct');if(!defined('WP_HTTP_BLOCK_EXTERNAL'))define('WP_HTTP_BLOCK_EXTERNAL',true);
require $wp_panel_root.'/wp-load.php';
if($wp_panel_action==='check') { global $wp_version;$wp_panel_send(is_string($wp_version)&&$wp_version===$wp_panel_target,$wp_version,is_string($wp_version)&&$wp_version===$wp_panel_target?'':'version_mismatch');exit; }
if($wp_panel_action==='upgrade_db'){require_once $wp_panel_root.'/wp-admin/includes/upgrade.php';wp_upgrade();delete_site_transient('update_core');global $wp_version;$wp_panel_send(true,$wp_version,'');exit;}
if($wp_panel_action!=='update'||!is_file($wp_panel_package)||!preg_match('/^[0-9a-f]{64}$/',$wp_panel_expected)||!hash_equals($wp_panel_expected,hash_file('sha256',$wp_panel_package))){$wp_panel_send(false,'','invalid_action');exit(2);}require_once $wp_panel_root.'/wp-admin/includes/file.php';require_once $wp_panel_root.'/wp-admin/includes/update.php';require_once $wp_panel_root.'/wp-admin/includes/class-wp-upgrader.php';
$wp_panel_packages=(object)['full'=>$wp_panel_package,'partial'=>'','new_bundled'=>'','no_content'=>'','rollback'=>''];$offer=(object)['response'=>'upgrade','version'=>$wp_panel_target,'current'=>$wp_panel_target,'partial_version'=>'','new_bundled'=>'','packages'=>$wp_panel_packages,'package'=>$wp_panel_package];
$upgrader=new Core_Upgrader(new Automatic_Upgrader_Skin());$result=$upgrader->upgrade($offer,['pre_check_md5'=>false,'attempt_rollback'=>false]);if(is_wp_error($result)){$wp_panel_error=(string)$result->get_error_code();if(!preg_match('/^[a-z0-9_]{1,64}$/',$wp_panel_error))$wp_panel_error='unknown';$wp_panel_send(false,'','core_upgrader_'.$wp_panel_error);exit(1);}$wp_panel_send($result===$wp_panel_target,(string)$result,$result===$wp_panel_target?'':'version_mismatch');`

const wpCoreUpdateRuntimeRoot = "/var/wp-panel/update-runtime"

type wpCorePHPRunnerOptions struct {
	wwwRoot, runtimeRoot, phpPath, runuserPath, phpDir, runuserDir string
	requireRoot                                                    bool
	ownerUID, ownerGID                                             int
	lookupUser                                                     func(string) (*user.User, error)
	chown                                                          func(string, int, int) error
}

type wpCorePHPRunner struct{ opts wpCorePHPRunnerOptions }

func newDefaultWPCorePHPRunner(wwwRoot string) (*wpCorePHPRunner, error) {
	return newWPCorePHPRunner(wpCorePHPRunnerOptions{wwwRoot: wwwRoot, runtimeRoot: wpCoreUpdateRuntimeRoot, phpPath: wpInventoryPHPPath, runuserPath: wpInventoryRunuserPath, phpDir: "/usr/bin", runuserDir: "/usr/sbin", requireRoot: true, ownerUID: 0, ownerGID: 0, lookupUser: user.Lookup, chown: os.Chown})
}

func newWPCorePHPRunner(opts wpCorePHPRunnerOptions) (*wpCorePHPRunner, error) {
	if opts.wwwRoot == "" || opts.runtimeRoot == "" || opts.phpPath == "" || opts.runuserPath == "" || opts.phpDir == "" || opts.runuserDir == "" || opts.lookupUser == nil || opts.chown == nil {
		return nil, errors.New("invalid core PHP runner")
	}
	return &wpCorePHPRunner{opts: opts}, nil
}

func (r *wpCorePHPRunner) Update(ctx context.Context, execution wpCoreUpdateExecution) error {
	input, err := r.validate(execution)
	if err != nil {
		return err
	}
	runtimeDir, packagePath, runtimeSHA, err := r.prepareRuntimePackage(execution, input)
	if err != nil {
		return err
	}
	defer os.RemoveAll(runtimeDir)
	if err := r.execute(ctx, input, "update", packagePath, execution.Task.TargetVersion, runtimeSHA); err != nil {
		return err
	}
	return r.execute(ctx, input, "upgrade_db", "", execution.Task.TargetVersion, "")
}

func (r *wpCorePHPRunner) CheckLoad(ctx context.Context, execution wpCoreUpdateExecution, version string) error {
	input, err := r.validate(execution)
	if err != nil {
		return err
	}
	return r.execute(ctx, input, "check", "", version, "")
}

type wpCoreRunnerInput struct {
	root, user, home, php, runuser string
	uid, gid                       int
}

func (r *wpCorePHPRunner) validate(execution wpCoreUpdateExecution) (wpCoreRunnerInput, error) {
	if r.opts.requireRoot && os.Geteuid() != 0 {
		return wpCoreRunnerInput{}, errors.New("core runner requires root")
	}
	if !wpInventoryUserPattern.MatchString(execution.SystemUser) {
		return wpCoreRunnerInput{}, errors.New("invalid site user")
	}
	root, err := validateInventorySitePath(r.opts.wwwRoot, execution.WebRoot)
	if err != nil {
		return wpCoreRunnerInput{}, err
	}
	u, err := r.opts.lookupUser(execution.SystemUser)
	if err != nil {
		return wpCoreRunnerInput{}, err
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil || uid <= 0 || gid <= 0 || u.Username != execution.SystemUser {
		return wpCoreRunnerInput{}, errors.New("invalid site identity")
	}
	php, err := validateInventoryBinary(r.opts.phpPath, r.opts.phpDir, r.opts.ownerUID, r.opts.ownerGID)
	if err != nil {
		return wpCoreRunnerInput{}, err
	}
	runuser, err := validateInventoryBinary(r.opts.runuserPath, r.opts.runuserDir, r.opts.ownerUID, r.opts.ownerGID)
	if err != nil {
		return wpCoreRunnerInput{}, err
	}
	return wpCoreRunnerInput{root: root, user: u.Username, home: u.HomeDir, uid: uid, gid: gid, php: php, runuser: runuser}, nil
}

func (r *wpCorePHPRunner) prepareRuntimePackage(execution wpCoreUpdateExecution, input wpCoreRunnerInput) (string, string, string, error) {
	if err := os.MkdirAll(r.opts.runtimeRoot, 0711); err != nil {
		return "", "", "", err
	}
	if info, err := os.Lstat(r.opts.runtimeRoot); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", "", errors.New("invalid core runtime root")
	}
	dir := filepath.Join(r.opts.runtimeRoot, execution.Task.ID)
	if filepath.Dir(dir) != filepath.Clean(r.opts.runtimeRoot) {
		return "", "", "", errors.New("invalid runtime directory")
	}
	if err := os.Mkdir(dir, 0710); err != nil {
		return "", "", "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := r.opts.chown(dir, r.opts.ownerUID, input.gid); err != nil {
		return "", "", "", err
	}
	target := filepath.Join(dir, "package.zip")
	sourceSHA, _, err := hashRegularFile(execution.PackagePath)
	if err != nil || sourceSHA != execution.Task.DownloadedSHA256 {
		return "", "", "", errors.New("runtime package source changed")
	}
	runtimeSHA, err := createWPCoreNoContentPackage(execution.PackagePath, target)
	if err != nil {
		return "", "", "", errors.New("runtime package copy failed")
	}
	if err := r.opts.chown(target, r.opts.ownerUID, input.gid); err != nil {
		return "", "", "", err
	}
	keep = true
	return dir, target, runtimeSHA, nil
}

func createWPCoreNoContentPackage(source, target string) (string, error) {
	zr, err := zip.OpenReader(source)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		_ = dst.Close()
		if !keep {
			_ = os.Remove(target)
		}
	}()
	zw := zip.NewWriter(dst)
	for _, entry := range zr.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if name == "wordpress/wp-content" || strings.HasPrefix(name, "wordpress/wp-content/") {
			continue
		}
		header := entry.FileHeader
		writer, err := zw.CreateHeader(&header)
		if err != nil {
			return "", err
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		src, err := entry.Open()
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(writer, src)
		closeErr := src.Close()
		if copyErr != nil || closeErr != nil {
			return "", errors.New("runtime package copy failed")
		}
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	if err := dst.Sync(); err != nil {
		return "", err
	}
	if err := dst.Chmod(0440); err != nil {
		return "", err
	}
	if err := dst.Close(); err != nil {
		return "", err
	}
	sha, _, err := hashRegularFile(target)
	if err != nil {
		return "", err
	}
	keep = true
	return sha, nil
}

type wpCoreRunnerEnvelope struct {
	Token     string `json:"token"`
	OK        bool   `json:"ok"`
	Version   string `json:"version"`
	ErrorCode string `json:"error_code"`
}

func (r *wpCorePHPRunner) execute(ctx context.Context, input wpCoreRunnerInput, action, packagePath, target, expectedSHA string) error {
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	token := hex.EncodeToString(tokenBytes)
	openBase := strings.Join([]string{input.root, r.opts.runtimeRoot, "/tmp", "/usr/share/php"}, ":")
	args := []string{"-u", input.user, "--", input.php, "-d", "open_basedir=" + openBase, "-d", "disable_functions=" + sitePHPDisabledFunctions(), "-d", "allow_url_include=0", "-d", "display_errors=0", "-d", "memory_limit=512M", "-d", "max_execution_time=300", "-d", "max_input_time=300", "-r", wpCoreUpdatePHPSource, action, input.root, packagePath, target, expectedSHA}
	execCtx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(execCtx, input.runuser, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=" + input.home, "USER=" + input.user, "LOGNAME=" + input.user, "TMPDIR=/tmp", "WP_PANEL_RUNNER_TOKEN=" + token}
	stdout := newCountingSink(64<<10, false)
	stderr := newCountingSink(64<<10, false)
	protocol := newCountingSink(64<<10, true)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readPipe.Close()
	cmd.ExtraFiles = []*os.File{writePipe}
	wpInventoryConfigureCommand(cmd)
	if err := cmd.Start(); err != nil {
		writePipe.Close()
		return errors.New("core PHP runner start failed")
	}
	_ = writePipe.Close()
	done := make(chan error, 1)
	go func() { _, err := io.Copy(protocol, readPipe); done <- err }()
	waitErr := cmd.Wait()
	copyErr := <-done
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if execCtx.Err() != nil {
		return errors.New("core PHP runner timed out")
	}
	if copyErr != nil {
		return errors.New("core PHP runner protocol failed")
	}
	_, stdoutExceeded, _ := stdout.snapshot()
	_, stderrExceeded, _ := stderr.snapshot()
	if stdoutExceeded || stderrExceeded {
		return errors.New("core PHP runner output exceeded")
	}
	_, exceeded, raw := protocol.snapshot()
	if exceeded {
		return errors.New("core PHP runner protocol exceeded")
	}
	var env wpCoreRunnerEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Token != token || !env.OK || env.Version != target || env.ErrorCode != "" || waitErr != nil {
		if env.Token == token && regexp.MustCompile(`^[a-z0-9_]{1,96}$`).MatchString(env.ErrorCode) {
			log.Printf("WordPress 核心 Runner 失败: %s", env.ErrorCode)
		}
		return errors.New("core PHP runner failed")
	}
	return nil
}
