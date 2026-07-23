package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	wpPluginUpdateRuntimeRoot = "/var/wp-panel/plugin-update-runtime"
	wpPluginResultName        = "runner-result.json"
	wpPluginResultMax         = 16 << 10
)

const wpPluginUpdatePHPSource = `
$action=$argv[1]??'';$root=$argv[2]??'';$runtime=$argv[3]??'';$package=$argv[4]??'';$plugin=$argv[5]??'';$current=$argv[6]??'';$target=$argv[7]??'';$expected_sha=$argv[8]??'';$journal=$argv[9]??'';$result_file=$argv[10]??'';$token=getenv('WP_PANEL_RUNNER_TOKEN');$sent=false;
$send=function($ok,$version='',$active=false,$code='')use(&$sent,$token,$result_file){if($sent)return;$sent=true;$body=json_encode(['token'=>$token,'ok'=>$ok,'version'=>$version,'active'=>(bool)$active,'error_code'=>$code],JSON_UNESCAPED_SLASHES);$f=@fopen($result_file,'c+b');if(!$f)return;@flock($f,LOCK_EX);@ftruncate($f,0);@fwrite($f,$body);@fflush($f);if(function_exists('fsync'))@fsync($f);@flock($f,LOCK_UN);@fclose($f);};
$checkpoint=function($name)use($journal){$f=@fopen($journal,'ab');if(!$f)return false;if(!@flock($f,LOCK_EX)){@fclose($f);return false;}$ok=@fwrite($f,$name."\n")===strlen($name)+1&&@fflush($f);if($ok&&function_exists('fsync'))$ok=@fsync($f);@flock($f,LOCK_UN);@fclose($f);return(bool)$ok;};
register_shutdown_function(function()use(&$sent,$send){if(!$sent){$send(false,'',false,error_get_last()?'fatal_error':'no_result');}});
if(PHP_SAPI!=='cli'||!preg_match('/^[0-9a-f]{32}$/D',$token)||!preg_match('#^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*\.php$#D',$plugin)||!preg_match('/^[0-9]+(?:\.[0-9]+){1,3}(?:[-+][A-Za-z0-9.-]+)?$/D',$current)||!preg_match('/^[0-9]+(?:\.[0-9]+){1,3}(?:[-+][A-Za-z0-9.-]+)?$/D',$target)||!is_dir($root)||realpath($root)!==rtrim($root,'/')||!is_dir($runtime)||realpath($runtime)!==rtrim($runtime,'/')||realpath(dirname($journal))!==$runtime||realpath(dirname($result_file))!==$runtime){$send(false,'',false,'invalid_input');exit(2);}
chdir($root);ob_start();if(!defined('WP_USE_THEMES'))define('WP_USE_THEMES',false);if(!defined('FS_METHOD'))define('FS_METHOD','direct');if(!defined('WP_HTTP_BLOCK_EXTERNAL'))define('WP_HTTP_BLOCK_EXTERNAL',true);require $root.'/wp-load.php';require_once $root.'/wp-admin/includes/plugin.php';
$main=WP_PLUGIN_DIR.'/'.$plugin;$data=is_file($main)?get_plugin_data($main,false,false):[];$version=is_array($data)&&isset($data['Version'])?(string)$data['Version']:'';$active=is_plugin_active($plugin);if(ob_get_length()>0){$send(false,$version,$active,'bootstrap_output');exit(1);}
if($action==='observe'){$send($version===$current,$version,$active,$version===$current?'':'version_mismatch');exit;}
if($action==='check'){$expect_active=($expected_sha==='active');$ok=$version===$target&&$active===$expect_active;$send($ok,$version,$active,$ok?'':'health_mismatch');exit($ok?0:1);}
if($action==='reactivate'){if($version!==$target||$active){$send(false,$version,$active,'reactivate_precheck_failed');exit(1);}if(!$checkpoint('reactivate_started')){$send(false,$version,$active,'journal_failed');exit(1);}$result=activate_plugin($plugin,'',false,true);$active=is_plugin_active($plugin);$data=is_file($main)?get_plugin_data($main,false,false):[];$version=is_array($data)&&isset($data['Version'])?(string)$data['Version']:'';if(is_wp_error($result)||$version!==$target||!$active||ob_get_length()>0){$send(false,$version,$active,'reactivate_failed');exit(1);}if(!$checkpoint('reactivate_completed')){$send(false,$version,$active,'journal_failed');exit(1);}$send(true,$version,$active,'');exit;}
if($action!=='update'||$version!==$current||!is_file($package)||realpath($package)!==$package||!preg_match('/^[0-9a-f]{64}$/D',$expected_sha)||!hash_equals($expected_sha,hash_file('sha256',$package))){$send(false,$version,$active,'update_precheck_failed');exit(1);}if(!$checkpoint('before_upgrade')){$send(false,$version,$active,'journal_failed');exit(1);}require_once $root.'/wp-admin/includes/file.php';require_once $root.'/wp-admin/includes/update.php';require_once $root.'/wp-admin/includes/class-wp-upgrader.php';$slug=strstr($plugin,'/',true);$updates=get_site_transient('update_plugins');if(!is_object($updates))$updates=new stdClass();if(!isset($updates->response)||!is_array($updates->response))$updates->response=[];$updates->response[$plugin]=(object)['id'=>'w.org/plugins/'.$slug,'slug'=>$slug,'plugin'=>$plugin,'new_version'=>$target,'url'=>'','package'=>$package,'icons'=>[],'banners'=>[],'banners_rtl'=>[],'tested'=>'','requires_php'=>''];set_site_transient('update_plugins',$updates);register_shutdown_function(function(){delete_site_transient('update_plugins');});if(!$checkpoint('upgrader_entered')){$send(false,$version,$active,'journal_failed');exit(1);}$upgrader=new Plugin_Upgrader(new Automatic_Upgrader_Skin());$upgrade_result=$upgrader->upgrade($plugin,['clear_update_cache'=>false]);if(!$checkpoint('upgrader_returned')){$send(false,'',false,'journal_failed');exit(1);}$data=is_file($main)?get_plugin_data($main,false,false):[];$version=is_array($data)&&isset($data['Version'])?(string)$data['Version']:'';$active=is_plugin_active($plugin);$ok=!is_wp_error($upgrade_result)&&$upgrade_result!==false&&$version===$target&&!$active&&ob_get_length()===0;$send($ok,$version,$active,$ok?'':'plugin_upgrader_failed');exit($ok?0:1);`

type wpPluginScopeRunner interface {
	Run(context.Context, string, ...string) error
}

type wpPluginPHPRunnerOptions struct {
	wwwRoot, runtimeRoot, phpPath, envPath, runuserPath string
	phpDir, envDir, runuserDir                          string
	requireRoot                                         bool
	ownerUID, ownerGID                                  int
	lookupUser                                          func(string) (*user.User, error)
	chown                                               func(string, int, int) error
	scope                                               wpPluginScopeRunner
}

type wpPluginPHPRunner struct{ opts wpPluginPHPRunnerOptions }

type wpPluginRunnerSession struct {
	runner                           *wpPluginPHPRunner
	execution                        wpPluginUpdateExecution
	root, user, home, php, env       string
	runtimeDir, packagePath, journal string
	result                           string
	mu                               sync.Mutex
	closed                           bool
}

type wpPluginRunnerEnvelope struct {
	Token     string `json:"token"`
	OK        bool   `json:"ok"`
	Version   string `json:"version"`
	Active    bool   `json:"active"`
	ErrorCode string `json:"error_code"`
}

func newDefaultWPPluginPHPRunner(wwwRoot string) (*wpPluginPHPRunner, error) {
	scope, err := newDefaultWPPluginUpdateScope(wpInventoryRunuserPath)
	if err != nil {
		return nil, err
	}
	return newWPPluginPHPRunner(wpPluginPHPRunnerOptions{
		wwwRoot: wwwRoot, runtimeRoot: wpPluginUpdateRuntimeRoot,
		phpPath: wpInventoryPHPPath, envPath: "/usr/bin/env", runuserPath: wpInventoryRunuserPath,
		phpDir: "/usr/bin", envDir: "/usr/bin", runuserDir: "/usr/sbin",
		requireRoot: true, ownerUID: 0, ownerGID: 0, lookupUser: user.Lookup, chown: os.Chown, scope: scope,
	})
}

func newWPPluginPHPRunner(opts wpPluginPHPRunnerOptions) (*wpPluginPHPRunner, error) {
	if !filepath.IsAbs(opts.wwwRoot) || !filepath.IsAbs(opts.runtimeRoot) || opts.lookupUser == nil || opts.chown == nil || opts.scope == nil || opts.phpPath == "" || opts.envPath == "" || opts.runuserPath == "" {
		return nil, errors.New("invalid plugin PHP runner")
	}
	return &wpPluginPHPRunner{opts: opts}, nil
}

func (r *wpPluginPHPRunner) Prepare(ctx context.Context, execution wpPluginUpdateExecution) (*wpPluginRunnerSession, error) {
	if r.opts.requireRoot && os.Geteuid() != 0 {
		return nil, errors.New("plugin runner requires root")
	}
	if !wpUpdateTaskIDPattern.MatchString(execution.Task.ID) || execution.Task.ComponentType != "plugin" || execution.Task.VerificationLevel != "structure_only" || !validWPPluginComponentKey(execution.Task.ComponentKey) || !wpInventoryUserPattern.MatchString(execution.SystemUser) || !wpUpdateSHA256Pattern.MatchString(execution.Task.DownloadedSHA256) || !wpCoreVersionPattern.MatchString(execution.Task.CurrentVersion) || !wpCoreVersionPattern.MatchString(execution.Task.TargetVersion) {
		return nil, errors.New("invalid plugin runner execution")
	}
	slug := strings.Split(execution.Task.ComponentKey, "/")[0]
	if _, err := ValidateWPComponentPackage(ctx, execution.PackagePath, WPComponentPackageExpectation{ComponentType: "plugin", ComponentKey: execution.Task.ComponentKey, OfficialSlug: slug, TargetVersion: execution.Task.TargetVersion}); err != nil {
		return nil, errors.New("plugin runner package validation failed")
	}
	root, err := validateInventorySitePath(r.opts.wwwRoot, execution.WebRoot)
	if err != nil {
		return nil, err
	}
	u, err := r.opts.lookupUser(execution.SystemUser)
	if err != nil {
		return nil, errors.New("plugin runner site identity unavailable")
	}
	uid, uidErr := strconv.Atoi(u.Uid)
	gid, gidErr := strconv.Atoi(u.Gid)
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 || u.Username != execution.SystemUser || !filepath.IsAbs(u.HomeDir) {
		return nil, errors.New("invalid plugin runner site identity")
	}
	php, err := validateInventoryBinary(r.opts.phpPath, r.opts.phpDir, r.opts.ownerUID, r.opts.ownerGID)
	if err != nil {
		return nil, err
	}
	env, err := validateInventoryBinary(r.opts.envPath, r.opts.envDir, r.opts.ownerUID, r.opts.ownerGID)
	if err != nil {
		return nil, err
	}
	if _, err := validateInventoryBinary(r.opts.runuserPath, r.opts.runuserDir, r.opts.ownerUID, r.opts.ownerGID); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(r.opts.runtimeRoot, 0711); err != nil {
		return nil, err
	}
	info, err := os.Lstat(r.opts.runtimeRoot)
	if err != nil {
		return nil, errors.New("invalid plugin runtime root")
	}
	uidOwner, gidOwner, ownerOK := wpInventoryFileOwner(info)
	realRuntime, realErr := filepath.EvalSymlinks(r.opts.runtimeRoot)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || !ownerOK || uidOwner != r.opts.ownerUID || gidOwner != r.opts.ownerGID || realErr != nil || realRuntime != filepath.Clean(r.opts.runtimeRoot) {
		return nil, errors.New("invalid plugin runtime root")
	}
	runtimeDir := filepath.Join(filepath.Clean(r.opts.runtimeRoot), execution.Task.ID)
	if filepath.Dir(runtimeDir) != filepath.Clean(r.opts.runtimeRoot) || os.Mkdir(runtimeDir, 0710) != nil {
		return nil, errors.New("plugin runtime directory unavailable")
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(runtimeDir)
		}
	}()
	if err := r.opts.chown(runtimeDir, r.opts.ownerUID, gid); err != nil {
		return nil, err
	}
	packagePath := filepath.Join(runtimeDir, "package.zip")
	sha, _, err := copyFileAtomic(execution.PackagePath, packagePath, 0440)
	if err != nil || sha != execution.Task.DownloadedSHA256 {
		return nil, errors.New("plugin runtime package changed")
	}
	if err := os.Chmod(packagePath, 0440); err != nil {
		return nil, err
	}
	if err := r.opts.chown(packagePath, r.opts.ownerUID, gid); err != nil {
		return nil, err
	}
	journal, err := createWPPluginUpdateJournal(runtimeDir, uid, gid)
	if err != nil {
		return nil, err
	}
	result := filepath.Join(runtimeDir, wpPluginResultName)
	if err := createWPPluginRunnerResult(result, uid, gid); err != nil {
		return nil, err
	}
	keep = true
	return &wpPluginRunnerSession{runner: r, execution: execution, root: root, user: u.Username, home: u.HomeDir, php: php, env: env, runtimeDir: runtimeDir, packagePath: packagePath, journal: journal, result: result}, nil
}

func createWPPluginRunnerResult(name string, uid, gid int) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chown(uid, gid); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (s *wpPluginRunnerSession) Observe(ctx context.Context) (bool, error) {
	env, err := s.execute(ctx, "observe", "", s.execution.Task.TargetVersion)
	return env.Active, err
}

func (s *wpPluginRunnerSession) Update(ctx context.Context) error {
	if _, err := s.execute(ctx, "update", s.execution.Task.DownloadedSHA256, s.execution.Task.TargetVersion); err != nil {
		return err
	}
	_, err := s.execute(ctx, "check", "inactive", s.execution.Task.TargetVersion)
	return err
}

func (s *wpPluginRunnerSession) Reactivate(ctx context.Context) error {
	_, err := s.execute(ctx, "reactivate", "", s.execution.Task.TargetVersion)
	return err
}

func (s *wpPluginRunnerSession) Check(ctx context.Context, version string, active bool) error {
	if !wpCoreVersionPattern.MatchString(version) {
		return errors.New("invalid plugin check version")
	}
	expected := "inactive"
	if active {
		expected = "active"
	}
	_, err := s.execute(ctx, "check", expected, version)
	return err
}

func (s *wpPluginRunnerSession) Journal() (wpPluginUpdateJournalReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return wpPluginUpdateJournalReport{}, errors.New("plugin runner session is closed")
	}
	return readWPPluginUpdateJournal(s.runtimeDir)
}

func (s *wpPluginRunnerSession) Close() error {
	if s == nil {
		return errors.New("invalid plugin runner session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runner == nil || !filepath.IsAbs(s.runtimeDir) || filepath.Dir(s.runtimeDir) != filepath.Clean(s.runner.opts.runtimeRoot) {
		return errors.New("invalid plugin runner session")
	}
	if s.closed {
		return nil
	}
	if err := os.RemoveAll(s.runtimeDir); err != nil {
		return err
	}
	s.closed = true
	return nil
}

func (s *wpPluginRunnerSession) execute(ctx context.Context, action, mode, expectedVersion string) (wpPluginRunnerEnvelope, error) {
	if s == nil || s.runner == nil {
		return wpPluginRunnerEnvelope{}, errors.New("invalid plugin runner session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return wpPluginRunnerEnvelope{}, errors.New("plugin runner session is closed")
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return wpPluginRunnerEnvelope{}, err
	}
	token := hex.EncodeToString(tokenBytes)
	if err := os.WriteFile(s.result, nil, 0600); err != nil {
		return wpPluginRunnerEnvelope{}, err
	}
	openBase := strings.Join([]string{s.root, s.runtimeDir, "/tmp", "/usr/share/php"}, ":")
	args := []string{"-u", s.user, "--", s.env, "-i", "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=" + s.home, "USER=" + s.user, "LOGNAME=" + s.user, "TMPDIR=/tmp", "WP_PANEL_RUNNER_TOKEN=" + token, s.php,
		"-d", "open_basedir=" + openBase, "-d", "disable_functions=" + sitePHPDisabledFunctions(), "-d", "allow_url_include=0", "-d", "display_errors=0", "-d", "memory_limit=256M", "-r", wpPluginUpdatePHPSource,
		action, s.root, s.runtimeDir, s.packagePath, s.execution.Task.ComponentKey, s.execution.Task.CurrentVersion, expectedVersion, mode, s.journal, s.result}
	runErr := s.runner.opts.scope.Run(ctx, s.execution.Task.ID, args...)
	if errors.Is(runErr, errWPPluginScopeSupervisionUncertain) {
		return wpPluginRunnerEnvelope{}, runErr
	}
	env, resultErr := readWPPluginRunnerResult(s.result, token)
	if runErr != nil || resultErr != nil || !env.OK || env.ErrorCode != "" || env.Version != expectedVersion && action != "observe" {
		return wpPluginRunnerEnvelope{}, errors.New("plugin PHP runner failed")
	}
	return env, nil
}

func readWPPluginRunnerResult(name, token string) (wpPluginRunnerEnvelope, error) {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > wpPluginResultMax {
		return wpPluginRunnerEnvelope{}, errors.New("invalid plugin runner result")
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		return wpPluginRunnerEnvelope{}, err
	}
	var env wpPluginRunnerEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Token != token {
		return wpPluginRunnerEnvelope{}, errors.New("invalid plugin runner envelope")
	}
	return env, nil
}
