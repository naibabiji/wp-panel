package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
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
$wp_panel_action=$argv[1]??'';$wp_panel_root=$argv[2]??'';$runtime=$argv[3]??'';$wp_panel_package=$argv[4]??'';$wp_panel_plugin=$argv[5]??'';$wp_panel_current=$argv[6]??'';$wp_panel_target=$argv[7]??'';$wp_panel_expected_sha=$argv[8]??'';$journal=$argv[9]??'';$result_file=$argv[10]??'';$token=getenv('WP_PANEL_RUNNER_TOKEN');$sent=false;
$send=function($ok,$version='',$active=false,$code='')use(&$sent,$token,$result_file){if($sent)return;$sent=true;$body=json_encode(['token'=>$token,'ok'=>$ok,'version'=>$version,'active'=>(bool)$active,'error_code'=>$code],JSON_UNESCAPED_SLASHES);$f=@fopen($result_file,'c+b');if(!$f)return;@flock($f,LOCK_EX);@ftruncate($f,0);@fwrite($f,$body);@fflush($f);if(function_exists('fsync'))@fsync($f);@flock($f,LOCK_UN);@fclose($f);};
$checkpoint=function($name)use($journal){$f=@fopen($journal,'ab');if(!$f)return false;if(!@flock($f,LOCK_EX)){@fclose($f);return false;}$ok=@fwrite($f,$name."\n")===strlen($name)+1&&@fflush($f);if($ok&&function_exists('fsync'))$ok=@fsync($f);@flock($f,LOCK_UN);@fclose($f);return(bool)$ok;};
register_shutdown_function(function()use(&$sent,$send){if(!$sent){$send(false,'',false,error_get_last()?'fatal_error':'no_result');}});
if(PHP_SAPI!=='cli'||!preg_match('/^[0-9a-f]{32}$/D',$token)||!preg_match('#^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*\.php$#D',$wp_panel_plugin)||!preg_match('/^[0-9]+(?:\.[0-9]+){1,3}(?:[-+][A-Za-z0-9.-]+)?$/D',$wp_panel_current)||!preg_match('/^[0-9]+(?:\.[0-9]+){1,3}(?:[-+][A-Za-z0-9.-]+)?$/D',$wp_panel_target)||!is_dir($wp_panel_root)||realpath($wp_panel_root)!==rtrim($wp_panel_root,'/')||!is_dir($runtime)||realpath($runtime)!==rtrim($runtime,'/')||realpath(dirname($journal))!==$runtime||realpath(dirname($result_file))!==$runtime){$send(false,'',false,'invalid_input');exit(2);}
chdir($wp_panel_root);ob_start();if(!defined('WP_USE_THEMES'))define('WP_USE_THEMES',false);if(!defined('FS_METHOD'))define('FS_METHOD','direct');if(!defined('WP_HTTP_BLOCK_EXTERNAL'))define('WP_HTTP_BLOCK_EXTERNAL',true);require $wp_panel_root.'/wp-load.php';require_once $wp_panel_root.'/wp-admin/includes/plugin.php';
$main=WP_PLUGIN_DIR.'/'.$wp_panel_plugin;$data=is_file($main)?get_plugin_data($main,false,false):[];$version=is_array($data)&&isset($data['Version'])?(string)$data['Version']:'';$active=is_plugin_active($wp_panel_plugin);if(ob_get_length()>0){$send(false,$version,$active,'bootstrap_output');exit(1);}
if($wp_panel_action==='observe'){$send($version===$wp_panel_current,$version,$active,$version===$wp_panel_current?'':'version_mismatch');exit;}
if($wp_panel_action==='check'){$expect_active=($wp_panel_expected_sha==='active');$ok=$version===$wp_panel_target&&$active===$expect_active;$send($ok,$version,$active,$ok?'':'health_mismatch');exit($ok?0:1);}
if($wp_panel_action==='reactivate'){if($version!==$wp_panel_target||$active){$send(false,$version,$active,'reactivate_precheck_failed');exit(1);}if(!$checkpoint('reactivate_started')){$send(false,$version,$active,'journal_failed');exit(1);}$result=activate_plugin($wp_panel_plugin,'',false,true);$active=is_plugin_active($wp_panel_plugin);$data=is_file($main)?get_plugin_data($main,false,false):[];$version=is_array($data)&&isset($data['Version'])?(string)$data['Version']:'';if(is_wp_error($result)||$version!==$wp_panel_target||!$active||ob_get_length()>0){$send(false,$version,$active,'reactivate_failed');exit(1);}if(!$checkpoint('reactivate_completed')){$send(false,$version,$active,'journal_failed');exit(1);}$send(true,$version,$active,'');exit;}
if($wp_panel_action!=='update'||$version!==$wp_panel_current||!is_file($wp_panel_package)||realpath($wp_panel_package)!==$wp_panel_package||!preg_match('/^[0-9a-f]{64}$/D',$wp_panel_expected_sha)||!hash_equals($wp_panel_expected_sha,hash_file('sha256',$wp_panel_package))){$send(false,$version,$active,'update_precheck_failed');exit(1);}if(!$checkpoint('before_upgrade')){$send(false,$version,$active,'journal_failed');exit(1);}require_once $wp_panel_root.'/wp-admin/includes/file.php';require_once $wp_panel_root.'/wp-admin/includes/update.php';require_once $wp_panel_root.'/wp-admin/includes/class-wp-upgrader.php';$slug=strstr($wp_panel_plugin,'/',true);$updates=get_site_transient('update_plugins');if(!is_object($updates))$updates=new stdClass();if(!isset($updates->response)||!is_array($updates->response))$updates->response=[];$original_updates=clone $updates;$wp_panel_update_succeeded=false;register_shutdown_function(function()use($original_updates,$wp_panel_plugin,&$wp_panel_update_succeeded){if($wp_panel_update_succeeded&&isset($original_updates->response)&&is_array($original_updates->response))unset($original_updates->response[$wp_panel_plugin]);set_site_transient('update_plugins',$original_updates);});$updates->response[$wp_panel_plugin]=(object)['id'=>'w.org/plugins/'.$slug,'slug'=>$slug,'plugin'=>$wp_panel_plugin,'new_version'=>$wp_panel_target,'url'=>'','package'=>$wp_panel_package,'icons'=>[],'banners'=>[],'banners_rtl'=>[],'tested'=>'','requires_php'=>''];set_site_transient('update_plugins',$updates);if(!$checkpoint('upgrader_entered')){$send(false,$version,$active,'journal_failed');exit(1);}$upgrader=new Plugin_Upgrader(new Automatic_Upgrader_Skin());$upgrade_result=$upgrader->upgrade($wp_panel_plugin,['clear_update_cache'=>false]);if(!$checkpoint('upgrader_returned')){$send(false,'',false,'journal_failed');exit(1);}$data=is_file($main)?get_plugin_data($main,false,false):[];$version=is_array($data)&&isset($data['Version'])?(string)$data['Version']:'';$active=is_plugin_active($wp_panel_plugin);$upgrade_output=ob_get_length();$ok=!is_wp_error($upgrade_result)&&$upgrade_result!==false&&$version===$wp_panel_target&&!$active&&$upgrade_output===0;$wp_panel_update_succeeded=$ok;$fail_reason='';if(!$ok){if(is_wp_error($upgrade_result)){$fail_reason=$upgrade_result->get_error_message();}elseif($upgrade_result===false){$fail_reason='upgrade_returned_false';}elseif($version!==$wp_panel_target){$fail_reason='version_still_'.$version;$pd=defined('WP_PLUGIN_DIR')?WP_PLUGIN_DIR:'(undef)';$pdr=@realpath($pd);$lr=@realpath($main);$lm=(int)@filemtime($main);$ls=(int)@filesize($main);$lh='';$lfp=@fopen($main,'r');if($lfp){$lb=fread($lfp,8192);fclose($lfp);if(preg_match('/Version:\s*([^\r\n*]+)/',$lb,$m))$lh=trim($m[1]);}$upd=defined('WP_CONTENT_DIR')?WP_CONTENT_DIR.'/upgrade':'/invalid';$ur=@realpath($upd);$uh=$upd.'/'.$slug;$has=is_dir($uh)?'yes':'no';$pkgv='';if(class_exists('ZipArchive')){$za=new ZipArchive();if($za->open($wp_panel_package)===true){$zfc=$za->getFromName($wp_panel_plugin);if($zfc!==false&&preg_match('/Version:\s*([^\r\n*]+)/',$zfc,$m))$pkgv=trim($m[1]);$za->close();}}$fail_reason.='|wp_plugin_dir='.$pd.' wp_plugin_dir_real='.$pdr.' live_real='.$lr.' live_mtime='.$lm.' live_size='.$ls.' live_header='.$lh.' upgrade_dir_real='.$ur.' upgrade_has_pkg='.$has.' pkg_header='.$pkgv;$manual='';$manual_fixed=false;if(function_exists('unzip_file')&&WP_Filesystem(array(),WP_PLUGIN_DIR)){global $wp_filesystem;$mres=unzip_file($wp_panel_package,WP_PLUGIN_DIR);if(is_wp_error($mres)){$manual='extract_failed:'.$mres->get_error_message();}else{$md=is_file($main)?get_plugin_data($main,false,false):[];$mv=is_array($md)&&isset($md['Version'])?(string)$md['Version']:'';$mh='';$mfp=@fopen($main,'r');if($mfp){$mb=fread($mfp,8192);fclose($mfp);if(preg_match('/Version:\s*([^\r\n*]+)/',$mb,$mm))$mh=trim($mm[1]);}if($mv===$wp_panel_target||$mh===$wp_panel_target){$manual_fixed=true;$version=$wp_panel_target;$active=is_plugin_active($wp_panel_plugin);$upgrade_output=ob_get_length();$ok=true;$wp_panel_update_succeeded=true;}else{$manual='extract_ok_but_version='.$mv.'/'.$mh;}}$manual.=' [fs='.(isset($wp_filesystem)?$wp_filesystem->method:'?').']';}else{$manual='unzip_file_unavailable';}if($manual_fixed){$fail_reason='';}else{$fail_reason.=' manual='.$manual;}}elseif($active){if(function_exists('deactivate_plugins')){deactivate_plugins($wp_panel_plugin,true);$active=is_plugin_active($wp_panel_plugin);$ok=!$active&&$version===$wp_panel_target&&!is_wp_error($upgrade_result)&&$upgrade_output===0;$wp_panel_update_succeeded=$ok;if(!$ok){$fail_reason='still_active';}}else{$fail_reason='still_active';}}elseif($upgrade_output>0){$fail_reason='unexpected_output';}else{$fail_reason='unknown_upgrader_state';}}if($ok&&$active){deactivate_plugins($wp_panel_plugin,true);$active=is_plugin_active($wp_panel_plugin);}$send($ok,$version,$active,$ok?'':('plugin_upgrader_failed'.($fail_reason!==''?': '.$fail_reason:'')));exit($ok?0:1);`

const wpThemeUpdatePHPSource = `
$wp_panel_action=$argv[1]??'';$wp_panel_root=$argv[2]??'';$runtime=$argv[3]??'';$wp_panel_package=$argv[4]??'';$wp_panel_stylesheet=$argv[5]??'';$wp_panel_current=$argv[6]??'';$wp_panel_target=$argv[7]??'';$wp_panel_mode=$argv[8]??'';$journal=$argv[9]??'';$result_file=$argv[10]??'';$wp_panel_expected_template=$argv[11]??'';$token=getenv('WP_PANEL_RUNNER_TOKEN');$sent=false;
$send=function($ok,$version='',$active=false,$code='')use(&$sent,$token,$result_file){if($sent)return;$sent=true;$body=json_encode(['token'=>$token,'ok'=>$ok,'version'=>$version,'active'=>(bool)$active,'error_code'=>$code],JSON_UNESCAPED_SLASHES);$f=@fopen($result_file,'c+b');if(!$f)return;@flock($f,LOCK_EX);@ftruncate($f,0);@fwrite($f,$body);@fflush($f);if(function_exists('fsync'))@fsync($f);@flock($f,LOCK_UN);@fclose($f);};
$checkpoint=function($name)use($journal){$f=@fopen($journal,'ab');if(!$f)return false;if(!@flock($f,LOCK_EX)){@fclose($f);return false;}$ok=@fwrite($f,$name."\n")===strlen($name)+1&&@fflush($f);if($ok&&function_exists('fsync'))$ok=@fsync($f);@flock($f,LOCK_UN);@fclose($f);return(bool)$ok;};
register_shutdown_function(function()use(&$sent,$send){if(!$sent){$send(false,'',false,error_get_last()?'fatal_error':'no_result');}});
if(PHP_SAPI!=='cli'||!preg_match('/^[0-9a-f]{32}$/D',$token)||!preg_match('/^[a-z0-9]+(?:-[a-z0-9]+)*$/D',$wp_panel_stylesheet)||($wp_panel_expected_template!==''&&!preg_match('/^[a-z0-9]+(?:-[a-z0-9]+)*$/D',$wp_panel_expected_template))||!preg_match('/^[0-9]+(?:\.[0-9]+){1,3}(?:[-+][A-Za-z0-9.-]+)?$/D',$wp_panel_current)||!preg_match('/^[0-9]+(?:\.[0-9]+){1,3}(?:[-+][A-Za-z0-9.-]+)?$/D',$wp_panel_target)||!is_dir($wp_panel_root)||realpath($wp_panel_root)!==rtrim($wp_panel_root,'/')||!is_dir($runtime)||realpath($runtime)!==rtrim($runtime,'/')||realpath(dirname($journal))!==$runtime||realpath(dirname($result_file))!==$runtime){$send(false,'',false,'invalid_input');exit(2);}
chdir($wp_panel_root);ob_start();if(!defined('WP_USE_THEMES'))define('WP_USE_THEMES',false);if(!defined('FS_METHOD'))define('FS_METHOD','direct');if(!defined('WP_HTTP_BLOCK_EXTERNAL'))define('WP_HTTP_BLOCK_EXTERNAL',true);require $wp_panel_root.'/wp-load.php';
$theme=wp_get_theme($wp_panel_stylesheet);$version=$theme->exists()?(string)$theme->get('Version'):'';$template=$theme->exists()?(string)$theme->get('Template'):'';$active=get_stylesheet()===$wp_panel_stylesheet;if(ob_get_length()>0){$send(false,$version,$active,'bootstrap_output');exit(1);}
if($wp_panel_action==='observe'){$ok=$version===$wp_panel_current&&$template===$wp_panel_expected_template;$send($ok,$version,$active,$ok?'':'identity_mismatch');exit($ok?0:1);}
if($wp_panel_action==='check'){$expect_active=($wp_panel_mode==='active');$ok=$version===$wp_panel_target&&$active===$expect_active&&$template===$wp_panel_expected_template;$send($ok,$version,$active,$ok?'':'health_mismatch');exit($ok?0:1);}
if($wp_panel_action!=='update'||$version!==$wp_panel_current||$template!==$wp_panel_expected_template||!is_file($wp_panel_package)||realpath($wp_panel_package)!==$wp_panel_package||!preg_match('/^[0-9a-f]{64}$/D',$wp_panel_mode)||!hash_equals($wp_panel_mode,hash_file('sha256',$wp_panel_package))){$send(false,$version,$active,'update_precheck_failed');exit(1);}if(!$checkpoint('before_upgrade')){$send(false,$version,$active,'journal_failed');exit(1);}require_once $wp_panel_root.'/wp-admin/includes/file.php';require_once $wp_panel_root.'/wp-admin/includes/update.php';require_once $wp_panel_root.'/wp-admin/includes/class-wp-upgrader.php';$updates=get_site_transient('update_themes');if(!is_object($updates))$updates=new stdClass();if(!isset($updates->response)||!is_array($updates->response))$updates->response=[];$original_updates=clone $updates;$wp_panel_update_succeeded=false;register_shutdown_function(function()use($original_updates,$wp_panel_stylesheet,&$wp_panel_update_succeeded){if($wp_panel_update_succeeded&&isset($original_updates->response)&&is_array($original_updates->response))unset($original_updates->response[$wp_panel_stylesheet]);set_site_transient('update_themes',$original_updates);});$updates->response[$wp_panel_stylesheet]=['theme'=>$wp_panel_stylesheet,'new_version'=>$wp_panel_target,'url'=>'','package'=>$wp_panel_package,'requires'=>'','requires_php'=>''];set_site_transient('update_themes',$updates);if(!$checkpoint('upgrader_entered')){$send(false,$version,$active,'journal_failed');exit(1);}$upgrader=new Theme_Upgrader(new Automatic_Upgrader_Skin());$upgrade_result=$upgrader->upgrade($wp_panel_stylesheet,['clear_update_cache'=>false]);if(!$checkpoint('upgrader_returned')){$send(false,'',false,'journal_failed');exit(1);}$theme=wp_get_theme($wp_panel_stylesheet);$version=$theme->exists()?(string)$theme->get('Version'):'';$template=$theme->exists()?(string)$theme->get('Template'):'';$active=get_stylesheet()===$wp_panel_stylesheet;$ok=!is_wp_error($upgrade_result)&&$upgrade_result!==false&&$version===$wp_panel_target&&$template===$wp_panel_expected_template&&ob_get_length()===0;$wp_panel_update_succeeded=$ok;$send($ok,$version,$active,$ok?'':'theme_upgrader_failed');exit($ok?0:1);`

type wpPluginScopeRunner interface {
	Run(context.Context, string, ...string) error
}

type wpPluginPHPRunnerOptions struct {
	wwwRoot, runtimeRoot, phpPath, envPath, runuserPath string
	phpDir, envDir, runuserDir                          string
	componentType, phpSource                            string
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
	expectedTemplate                 string
	observedActive                   bool
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

func newDefaultWPThemePHPRunner(wwwRoot string) (*wpPluginPHPRunner, error) {
	scope, err := newDefaultWPPluginUpdateScope(wpInventoryRunuserPath)
	if err != nil {
		return nil, err
	}
	return newWPPluginPHPRunner(wpPluginPHPRunnerOptions{
		wwwRoot: wwwRoot, runtimeRoot: wpPluginUpdateRuntimeRoot,
		phpPath: wpInventoryPHPPath, envPath: "/usr/bin/env", runuserPath: wpInventoryRunuserPath,
		phpDir: "/usr/bin", envDir: "/usr/bin", runuserDir: "/usr/sbin",
		componentType: "theme", phpSource: wpThemeUpdatePHPSource,
		requireRoot: true, ownerUID: 0, ownerGID: 0, lookupUser: user.Lookup, chown: os.Chown, scope: scope,
	})
}

func newWPPluginPHPRunner(opts wpPluginPHPRunnerOptions) (*wpPluginPHPRunner, error) {
	if !filepath.IsAbs(opts.wwwRoot) || !filepath.IsAbs(opts.runtimeRoot) || opts.lookupUser == nil || opts.chown == nil || opts.scope == nil || opts.phpPath == "" || opts.envPath == "" || opts.runuserPath == "" {
		return nil, errors.New("invalid plugin PHP runner")
	}
	if opts.componentType == "" {
		opts.componentType = "plugin"
	}
	if opts.phpSource == "" {
		opts.phpSource = wpPluginUpdatePHPSource
	}
	if (opts.componentType != "plugin" && opts.componentType != "theme") || opts.phpSource == "" {
		return nil, errors.New("invalid component PHP runner")
	}
	return &wpPluginPHPRunner{opts: opts}, nil
}

func (r *wpPluginPHPRunner) Prepare(ctx context.Context, execution wpPluginUpdateExecution) (*wpPluginRunnerSession, error) {
	if r.opts.requireRoot && os.Geteuid() != 0 {
		return nil, errors.New("plugin runner requires root")
	}
	validComponent := execution.Task.ComponentType == r.opts.componentType &&
		(execution.Task.ComponentType == "plugin" && validWPPluginComponentKey(execution.Task.ComponentKey) ||
			execution.Task.ComponentType == "theme" && validWPThemeComponentKey(execution.Task.ComponentKey))
	if !wpUpdateTaskIDPattern.MatchString(execution.Task.ID) || !validComponent || execution.Task.VerificationLevel != "structure_only" || !wpInventoryUserPattern.MatchString(execution.SystemUser) || !wpUpdateSHA256Pattern.MatchString(execution.Task.DownloadedSHA256) || !wpCoreVersionPattern.MatchString(execution.Task.CurrentVersion) || !wpCoreVersionPattern.MatchString(execution.Task.TargetVersion) {
		return nil, errors.New("invalid plugin runner execution")
	}
	slug, template := execution.Task.ComponentKey, ""
	var err error
	if execution.Task.ComponentType == "plugin" {
		slug = strings.Split(execution.Task.ComponentKey, "/")[0]
	} else {
		_, template, err = readInstalledWPThemeIdentity(execution.WebRoot, execution.Task.ComponentKey)
		if err != nil {
			return nil, errors.New("theme runner identity unavailable")
		}
	}
	if _, err := ValidateWPComponentPackage(ctx, execution.PackagePath, WPComponentPackageExpectation{ComponentType: execution.Task.ComponentType, ComponentKey: execution.Task.ComponentKey, OfficialSlug: slug, TargetVersion: execution.Task.TargetVersion, Template: template}); err != nil {
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
	return &wpPluginRunnerSession{runner: r, execution: execution, root: root, user: u.Username, home: u.HomeDir, php: php, env: env, runtimeDir: runtimeDir, packagePath: packagePath, journal: journal, result: result, expectedTemplate: template}, nil
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
	if err == nil {
		s.observedActive = env.Active
	}
	return env.Active, err
}

func (s *wpPluginRunnerSession) Update(ctx context.Context) error {
	if _, err := s.execute(ctx, "update", s.execution.Task.DownloadedSHA256, s.execution.Task.TargetVersion); err != nil {
		return err
	}
	expected := "inactive"
	if s.execution.Task.ComponentType == "theme" && s.observedActive {
		expected = "active"
	}
	_, err := s.execute(ctx, "check", expected, s.execution.Task.TargetVersion)
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
		"-d", "open_basedir=" + openBase, "-d", "disable_functions=" + sitePHPDisabledFunctions(), "-d", "allow_url_include=0", "-d", "display_errors=0", "-d", "memory_limit=512M", "-d", "max_execution_time=300", "-d", "max_input_time=300", "-r", s.runner.opts.phpSource,
		action, s.root, s.runtimeDir, s.packagePath, s.execution.Task.ComponentKey, s.execution.Task.CurrentVersion, expectedVersion, mode, s.journal, s.result}
	if s.execution.Task.ComponentType == "theme" {
		args = append(args, s.expectedTemplate)
	}
	runErr := s.runner.opts.scope.Run(ctx, s.execution.Task.ID, args...)
	if errors.Is(runErr, errWPPluginScopeSupervisionUncertain) {
		return wpPluginRunnerEnvelope{}, runErr
	}
	env, resultErr := readWPPluginRunnerResult(s.result, token)
	if runErr != nil || resultErr != nil || !env.OK || env.ErrorCode != "" || env.Version != expectedVersion && action != "observe" {
		log.Printf("插件更新 PHP runner 失败 site=%s 组件=%s 动作=%s: runErr=%v resultErr=%v ok=%v errorCode=%q version=%q expected=%q",
			s.execution.Domain, s.execution.Task.ComponentKey, action, runErr, resultErr, env.OK, env.ErrorCode, env.Version, expectedVersion)
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
