<?php
define('ABSPATH', '/test/');
class Reply extends Exception { public $value; public function __construct($value) { $this->value=$value; } }
function wp_send_json_error($value,$status=400) { throw new Reply(['success'=>false,'data'=>$value]); }
function wp_send_json_success($value) { throw new Reply(['success'=>true,'data'=>$value]); }
function current_user_can($cap) { return $cap==='manage_options' && $GLOBALS['authorized']; }
function is_multisite() { return false; }
function check_ajax_referer($action,$key) { if (($_POST[$key]??'')!=='valid') wp_send_json_error(['message'=>'nonce_failed'],403); }
function sanitize_key($s) { return preg_replace('/[^a-z0-9_-]/','',strtolower($s)); }
function wp_unslash($s) { return stripslashes($s); }
function absint($v) { return abs((int)$v); }
function get_current_user_id() { return 42; }
function wp_json_encode($v) { return json_encode($v); }
function wp_remote_request($url,$args) { $GLOBALS['calls'][]=[$url,$args];return ['body'=>json_encode(['success'=>true,'data'=>['state'=>'locked']])]; }
function is_wp_error($v) { return false; }
function wp_remote_retrieve_body($v) { return $v['body']; }
function delete_transient($key) {}
require __DIR__.'/../../wp-panel-optimizer/includes/trait-maintenance.php';
class Fixture {
    use WPP_Optimizer_Maintenance_Trait;
    const FILE_LOCK_STATE_TRANSIENT='state';
    private static function get_panel_url() { return $GLOBALS['url']; }
    private static function get_api_key() { return str_repeat('a',64); }
    private static function update_file_lock_state_option($v) {}
}
function verify($condition,$message) { if (!$condition) throw new Exception($message); }
function run() { try { Fixture::maintenance_ajax(); } catch (Reply $r) { return $r->value; } throw new Exception('missing response'); }
$authorized=true;$url='https://127.0.0.1:8443/random-prefix';$calls=[];
$_POST=['operation'=>'unlock','nonce'=>'valid','request_id'=>'abc','password'=>'secret-in-request'];
$authorized=false;verify(!run()['success'] && !$calls,'capability gate');
$authorized=true;$_POST['nonce']='bad';verify(!run()['success'] && !$calls,'nonce gate');
$_POST['nonce']='valid';$url='https://attacker.example/path';verify(!run()['success'] && !$calls,'origin gate');
$url='https://127.0.0.1:8443/random-prefix';verify(run()['success'],'valid request');
verify(count($calls)===1,'request count');
verify($calls[0][1]['redirection']===0,'no redirects');
verify($calls[0][0]===$url.'/api/sites/maintenance/unlock','fixed path');
$payload=json_decode($calls[0][1]['body'],true);
verify($payload['actor']==='42' && !isset($payload['site_id']) && !isset($payload['domain']),'site identity');
verify($payload['password']==='secret-in-request','password only forwarded');
$_POST['operation']='shell';verify(!run()['success'] && count($calls)===1,'operation allowlist');
echo "maintenance PHP checks passed\n";
