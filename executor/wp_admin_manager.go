package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/models"
)

const wpAdminManagerPHPSource = `
$wp_panel_token=getenv('WP_PANEL_RUNNER_TOKEN');$wp_panel_sent=false;
$wp_panel_send=function($ok,$data=[],$code='')use(&$wp_panel_sent,$wp_panel_token){if($wp_panel_sent)return;$wp_panel_sent=true;file_put_contents('php://fd/3',json_encode(['token'=>$wp_panel_token,'ok'=>$ok,'data'=>$data,'error_code'=>$code],JSON_UNESCAPED_SLASHES));};
register_shutdown_function(function()use(&$wp_panel_sent,$wp_panel_send){if(!$wp_panel_sent){$e=error_get_last();$wp_panel_send(false,[],$e?'fatal_error':'no_result');}});
$wp_panel_raw=stream_get_contents(STDIN,65537);$wp_panel_input=json_decode($wp_panel_raw,true);
if(PHP_SAPI!=='cli'||!preg_match('/^[0-9a-f]{32}$/',$wp_panel_token)||strlen($wp_panel_raw)>65536||!is_array($wp_panel_input)){$wp_panel_send(false,[],'invalid_input');exit(2);}
$wp_panel_root=$wp_panel_input['root']??'';$wp_panel_action=$wp_panel_input['action']??'';
if(!is_string($wp_panel_root)||!is_dir($wp_panel_root)||realpath($wp_panel_root)!==rtrim($wp_panel_root,'/')){$wp_panel_send(false,[],'invalid_root');exit(2);}
chdir($wp_panel_root);ob_start();if(!defined('WP_USE_THEMES'))define('WP_USE_THEMES',false);if(!defined('WP_HTTP_BLOCK_EXTERNAL'))define('WP_HTTP_BLOCK_EXTERNAL',true);require $wp_panel_root.'/wp-load.php';ob_end_clean();
global $wpdb;
if(is_multisite()){$wp_panel_send(false,[],'multisite_unsupported');exit(1);}
$wp_panel_expected_db=$wp_panel_input['db_name']??'';$wp_panel_expected_prefix=$wp_panel_input['table_prefix']??'';
if(!is_string($wp_panel_expected_db)||!is_string($wp_panel_expected_prefix)||DB_NAME!==$wp_panel_expected_db||$wpdb->prefix!==$wp_panel_expected_prefix){$wp_panel_send(false,[],'database_mismatch');exit(1);}
if($wp_panel_action==='list'){
  $wp_panel_users=get_users(['role'=>'administrator','orderby'=>'ID','order'=>'ASC']);$wp_panel_items=[];
  foreach($wp_panel_users as $wp_panel_user){$wp_panel_items[]=['id'=>(int)$wp_panel_user->ID,'login'=>$wp_panel_user->user_login,'email'=>$wp_panel_user->user_email,'display_name'=>$wp_panel_user->display_name,'nicename'=>$wp_panel_user->user_nicename];}
  $wp_panel_send(true,['administrators'=>$wp_panel_items,'site_admin_email'=>(string)get_option('admin_email')]);exit;
}
if($wp_panel_action!=='preflight'&&$wp_panel_action!=='update'){$wp_panel_send(false,[],'invalid_action');exit(2);}
$wp_panel_id=(int)($wp_panel_input['user_id']??0);$wp_panel_user=get_userdata($wp_panel_id);
if(!$wp_panel_user||!in_array('administrator',(array)$wp_panel_user->roles,true)){$wp_panel_send(false,[],'administrator_not_found');exit(1);}
$wp_panel_old_login=$wp_panel_user->user_login;$wp_panel_new_login=$wp_panel_input['login']??$wp_panel_old_login;
if(!is_string($wp_panel_new_login)||$wp_panel_new_login===''||mb_strlen($wp_panel_new_login)>60){$wp_panel_send(false,[],'invalid_login');exit(1);}
$wp_panel_sanitized=trim(apply_filters('pre_user_login',sanitize_user($wp_panel_new_login,true)));if($wp_panel_sanitized!==$wp_panel_new_login){$wp_panel_send(false,[],'invalid_login');exit(1);}
$wp_panel_illegal=(array)apply_filters('illegal_user_logins',[]);if(in_array(strtolower($wp_panel_new_login),array_map('strtolower',$wp_panel_illegal),true)){$wp_panel_send(false,[],'invalid_login');exit(1);}
$wp_panel_existing=username_exists($wp_panel_new_login);if($wp_panel_existing&&((int)$wp_panel_existing!==$wp_panel_id)){$wp_panel_send(false,[],'login_exists');exit(1);}
$wp_panel_email=$wp_panel_input['email']??$wp_panel_user->user_email;if(!is_string($wp_panel_email)||!is_email($wp_panel_email)){$wp_panel_send(false,[],'invalid_email');exit(1);}
$wp_panel_existing=email_exists($wp_panel_email);if($wp_panel_existing&&((int)$wp_panel_existing!==$wp_panel_id)){$wp_panel_send(false,[],'email_exists');exit(1);}
$wp_panel_display=$wp_panel_input['display_name']??$wp_panel_user->display_name;if(!is_string($wp_panel_display)||trim($wp_panel_display)===''||mb_strlen($wp_panel_display)>250){$wp_panel_send(false,[],'invalid_display_name');exit(1);}
$wp_panel_password=$wp_panel_input['password']??'';if(!is_string($wp_panel_password)||($wp_panel_password!==''&&strlen($wp_panel_password)<8)||strlen($wp_panel_password)>4096){$wp_panel_send(false,[],'invalid_password');exit(1);}
$wp_panel_sync_nicename=!empty($wp_panel_input['sync_nicename']);$wp_panel_sync_admin_email=!empty($wp_panel_input['sync_admin_email']);$wp_panel_destroy_sessions=!empty($wp_panel_input['destroy_sessions']);
$wp_panel_transactional_tables=[$wpdb->users,$wpdb->usermeta,$wpdb->options];foreach($wp_panel_transactional_tables as $wp_panel_table){$wp_panel_engine=$wpdb->get_var($wpdb->prepare('SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA=%s AND TABLE_NAME=%s',DB_NAME,$wp_panel_table));if(!is_string($wp_panel_engine)||strtoupper($wp_panel_engine)!=='INNODB'){$wp_panel_send(false,[],'non_transactional_engine');exit(1);}}
if($wp_panel_sync_nicename&&sanitize_title($wp_panel_new_login)===''){$wp_panel_send(false,[],'invalid_login');exit(1);}
if($wp_panel_action==='preflight'){$wp_panel_send(true,['validated'=>true]);exit;}
add_filter('send_password_change_email','__return_false',PHP_INT_MAX);add_filter('send_email_change_email','__return_false',PHP_INT_MAX);
$wp_panel_transaction=$wpdb->query('START TRANSACTION');if($wp_panel_transaction===false){$wp_panel_send(false,[],'transaction_failed');exit(1);}
$wp_panel_fail=function($code)use($wpdb,$wp_panel_send,$wp_panel_id,$wp_panel_old_login,$wp_panel_new_login){$wpdb->query('ROLLBACK');clean_user_cache($wp_panel_id);wp_cache_delete($wp_panel_old_login,'userlogins');wp_cache_delete($wp_panel_new_login,'userlogins');$wp_panel_send(false,[],$code);exit(1);};
$wp_panel_conflict=$wpdb->get_var($wpdb->prepare("SELECT ID FROM {$wpdb->users} WHERE user_login=%s AND ID<>%d LIMIT 1 FOR UPDATE",$wp_panel_new_login,$wp_panel_id));if($wp_panel_conflict)$wp_panel_fail('login_exists');
$wp_panel_update=['ID'=>$wp_panel_id,'user_email'=>$wp_panel_email,'display_name'=>trim($wp_panel_display)];
if($wp_panel_password!=='')$wp_panel_update['user_pass']=$wp_panel_password;
if($wp_panel_sync_nicename){$wp_panel_update['user_nicename']=sanitize_title($wp_panel_new_login);$wp_panel_update['nickname']=$wp_panel_new_login;}
$wp_panel_result=wp_update_user($wp_panel_update);if(is_wp_error($wp_panel_result))$wp_panel_fail('user_update_failed');
if($wp_panel_new_login!==$wp_panel_old_login){$wp_panel_changed=$wpdb->update($wpdb->users,['user_login'=>$wp_panel_new_login],['ID'=>$wp_panel_id],['%s'],['%d']);if($wp_panel_changed===false)$wp_panel_fail('login_update_failed');}
if($wp_panel_sync_admin_email){update_option('admin_email',$wp_panel_email);delete_option('new_admin_email');}
if($wp_panel_destroy_sessions||$wp_panel_password!==''||$wp_panel_new_login!==$wp_panel_old_login){WP_Session_Tokens::get_instance($wp_panel_id)->destroy_all();}
clean_user_cache($wp_panel_id);wp_cache_delete($wp_panel_old_login,'userlogins');wp_cache_delete($wp_panel_new_login,'userlogins');
$wp_panel_updated=get_userdata($wp_panel_id);if(!$wp_panel_updated||$wp_panel_updated->user_login!==$wp_panel_new_login||$wp_panel_updated->user_email!==$wp_panel_email)$wp_panel_fail('verification_failed');
if($wp_panel_password!==''&&!wp_check_password($wp_panel_password,$wp_panel_updated->user_pass,$wp_panel_id))$wp_panel_fail('password_verification_failed');
if($wpdb->query('COMMIT')===false)$wp_panel_fail('commit_failed');
$wp_panel_send(true,['administrator'=>['id'=>(int)$wp_panel_updated->ID,'login'=>$wp_panel_updated->user_login,'email'=>$wp_panel_updated->user_email,'display_name'=>$wp_panel_updated->display_name,'nicename'=>$wp_panel_updated->user_nicename],'site_admin_email'=>(string)get_option('admin_email')]);`

type WPAdministrator struct {
	ID          int    `json:"id"`
	Login       string `json:"login"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Nicename    string `json:"nicename"`
}

type WPAdministratorList struct {
	Administrators []WPAdministrator `json:"administrators"`
	SiteAdminEmail string            `json:"site_admin_email"`
}

type WPAdministratorResult struct {
	Administrator  WPAdministrator `json:"administrator"`
	SiteAdminEmail string          `json:"site_admin_email"`
}

type WPAdministratorUpdate struct {
	UserID          int    `json:"user_id"`
	Login           string `json:"login"`
	Password        string `json:"password,omitempty"`
	Email           string `json:"email"`
	DisplayName     string `json:"display_name"`
	SyncNicename    bool   `json:"sync_nicename"`
	SyncAdminEmail  bool   `json:"sync_admin_email"`
	DestroySessions bool   `json:"destroy_sessions"`
}

type wpAdminManagerInput struct {
	Action      string `json:"action"`
	Root        string `json:"root"`
	DBName      string `json:"db_name"`
	TablePrefix string `json:"table_prefix"`
	WPAdministratorUpdate
}

type wpAdminManagerEnvelope struct {
	Token     string          `json:"token"`
	OK        bool            `json:"ok"`
	Data      json.RawMessage `json:"data"`
	ErrorCode string          `json:"error_code"`
}

var wpAdminManagerErrorPattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

type WPAdminManagerError struct{ Code string }

func (e *WPAdminManagerError) Error() string { return "administrator runner: " + e.Code }

func WPAdminManagerErrorCode(err error) string {
	var managerErr *WPAdminManagerError
	if errors.As(err, &managerErr) {
		return managerErr.Code
	}
	return ""
}

func ListWPAdministrators(ctx context.Context, site *models.Website) (WPAdministratorList, error) {
	var result WPAdministratorList
	if err := runWPAdminManager(ctx, site, "list", WPAdministratorUpdate{}, &result); err != nil {
		return result, err
	}
	if result.Administrators == nil {
		result.Administrators = []WPAdministrator{}
	}
	return result, nil
}

func UpdateWPAdministrator(ctx context.Context, site *models.Website, update WPAdministratorUpdate) (WPAdministratorResult, error) {
	var result WPAdministratorResult
	if err := runWPAdminManager(ctx, site, "update", update, &result); err != nil {
		return result, err
	}
	return result, nil
}

func PreflightWPAdministratorUpdate(ctx context.Context, site *models.Website, update WPAdministratorUpdate) error {
	var result struct {
		Validated bool `json:"validated"`
	}
	if err := runWPAdminManager(ctx, site, "preflight", update, &result); err != nil {
		return err
	}
	if !result.Validated {
		return errors.New("administrator preflight response invalid")
	}
	return nil
}

func runWPAdminManager(ctx context.Context, site *models.Website, action string, update WPAdministratorUpdate, target interface{}) error {
	if site == nil || site.SiteType != "wordpress" || site.ID <= 0 || site.DBName == "" || !IsValidWPTablePrefix(site.TablePrefix) {
		return errors.New("invalid WordPress site")
	}
	cfg := config.AppConfig
	if cfg == nil {
		return errors.New("panel configuration unavailable")
	}
	runner, err := newDefaultWPCorePHPRunner(cfg.Paths.WWWRoot)
	if err != nil {
		return err
	}
	validated, err := runner.validate(wpCoreUpdateExecution{WebRoot: site.WebRoot, SystemUser: site.SystemUser})
	if err != nil {
		return err
	}
	input := wpAdminManagerInput{Action: action, Root: validated.root, DBName: site.DBName, TablePrefix: site.TablePrefix, WPAdministratorUpdate: update}
	payload, err := json.Marshal(input)
	if err != nil || len(payload) > 64<<10 {
		return errors.New("administrator request too large")
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	token := hex.EncodeToString(tokenBytes)
	args := []string{"-u", validated.user, "--", validated.php, "-d", "open_basedir=" + strings.Join([]string{validated.root, "/tmp", "/usr/share/php"}, ":"), "-d", "disable_functions=" + sitePHPDisabledFunctions(), "-d", "allow_url_include=0", "-d", "display_errors=0", "-d", "memory_limit=256M", "-r", wpAdminManagerPHPSource}
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(execCtx, validated.runuser, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=" + validated.home, "USER=" + validated.user, "LOGNAME=" + validated.user, "TMPDIR=/tmp", "WP_PANEL_RUNNER_TOKEN=" + token}
	cmd.Stdin = bytes.NewReader(payload)
	stdout, stderr, protocol := newCountingSink(64<<10, false), newCountingSink(64<<10, false), newCountingSink(64<<10, true)
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
		return errors.New("administrator runner start failed")
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
		return errors.New("administrator runner timed out")
	}
	_, stdoutExceeded, _ := stdout.snapshot()
	_, stderrExceeded, _ := stderr.snapshot()
	_, protocolExceeded, raw := protocol.snapshot()
	if copyErr != nil || stdoutExceeded || stderrExceeded || protocolExceeded {
		return errors.New("administrator runner output invalid")
	}
	var envelope wpAdminManagerEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Token != token || waitErr != nil || !envelope.OK {
		if envelope.Token == token && wpAdminManagerErrorPattern.MatchString(envelope.ErrorCode) {
			return &WPAdminManagerError{Code: envelope.ErrorCode}
		}
		return errors.New("administrator runner failed")
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return errors.New("administrator runner response invalid")
	}
	return nil
}
