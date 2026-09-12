<?php
define('ABSPATH', '/fixture/');
define('DAY_IN_SECONDS', 86400);
define('ARRAY_A', 'ARRAY_A');
define('DB_NAME', 'fixture_db');
require __DIR__.'/../../wp-panel-optimizer/includes/trait-anomaly-monitor.php';
class MonitorFixture { use WPP_Optimizer_Anomaly_Monitor_Trait; }
function verify($ok, $message) { if (!$ok) throw new Exception($message); }
try { MonitorFixture::collect_anomaly_sample(0, 100, []); throw new Exception('missing CLI runner gate'); }
catch (RuntimeException $e) { verify($e->getMessage()==='runner_required', 'runner gate'); }
define('WP_PANEL_INVENTORY_RUNNER', true);
$multisite=false;
function is_multisite() { return $GLOBALS['multisite']; }
function wp_salt($scheme) { return 'test-only-site-salt'; }
function wp_json_encode($value) { return json_encode($value); }
function get_option($name, $default=false) { return $GLOBALS['options'][$name] ?? $default; }
function get_users($args) {
    verify($args['role']==='administrator' && $args['number']===101, 'bounded administrator query');
    return $GLOBALS['users'];
}
function get_userdata($id) { return $id===2 ? (object)['ID'=>2] : false; }
class WP_Application_Passwords {
    static function get_user_application_passwords($id) { return $GLOBALS['application_passwords'][$id] ?? []; }
}
class SampleDB {
    public $posts='custom_posts'; public $last_error=''; public $params; public $rows=[]; public $content_rows=[]; public $content_after=0; public $database_rows=[];
    function prepare($sql, ...$args) {
		if (strpos($sql,'information_schema.')!==false) { verify($args===['fixture_db'], 'database schema bound'); return $sql; }
        if (strpos($sql,'ID > %d')!==false) { verify(strpos($sql,'LIMIT 251')!==false, 'bounded content batch'); $this->content_after=(int)$args[0]; return $sql; }
        verify(strpos($sql, "post_type IN ('post','page')")!==false && strpos($sql,"post_status = 'publish'")!==false, 'published posts and pages only');
        verify(strpos($sql,'custom_posts')!==false, 'actual table prefix');
        $this->params=$args; return $sql;
    }
    function get_var($sql) {
        $count=0;
        foreach ($this->rows as $row) if (in_array($row[0],['post','page'],true) && $row[1]==='publish' && $row[2]>$this->params[0] && $row[2]<=$this->params[1]) $count++;
        return (string)$count;
    }
    function get_results($sql, $format) {
		if (strpos($sql,'information_schema.TRIGGERS')!==false) return $this->database_rows['trigger'] ?? [];
		if (strpos($sql,'information_schema.EVENTS')!==false) return $this->database_rows['event'] ?? [];
		if (strpos($sql,"ROUTINE_TYPE='PROCEDURE'")!==false) return $this->database_rows['procedure'] ?? [];
		if (strpos($sql,"ROUTINE_TYPE='FUNCTION'")!==false) return $this->database_rows['function'] ?? [];
        verify($format===ARRAY_A && strpos($sql,"post_type IN ('post','page')")!==false && strpos($sql,'LIMIT 251')!==false, 'bounded content snapshot');
        return array_slice(array_values(array_filter($this->content_rows, fn($row)=>(int)$row['ID']>$this->content_after)),0,251);
    }
}
$wpdb=new SampleDB();
$users=[(object)['ID'=>1,'user_login'=>'owner','user_email'=>'Owner@Example.com','display_name'=>'Owner','user_pass'=>'portable-hash','roles'=>['editor','administrator']]];
$base_user=$users[0];
$application_passwords=[1=>[['uuid'=>'secret-uuid','app_id'=>'','name'=>'Automation','password'=>'$P$secret-hash','created'=>1700000000,'last_used'=>1700000100,'last_ip'=>'192.0.2.10']]];
$options=['siteurl'=>'https://example.com/wp','home'=>'https://example.com','users_can_register'=>'1','default_role'=>'subscriber','page_on_front'=>'9'];
$until=1800000000;
$wpdb->rows=[['post','publish',gmdate('Y-m-d H:i:s',$until-1)], ['page','publish',gmdate('Y-m-d H:i:s',$until-2)], ['post','publish',gmdate('Y-m-d H:i:s',$until-86400)],
    ['post','draft',gmdate('Y-m-d H:i:s',$until-1)], ['product','publish',gmdate('Y-m-d H:i:s',$until-1)],
    ['post','publish',gmdate('Y-m-d H:i:s',$until+1)]];
$wpdb->content_rows=[['ID'=>'9','post_type'=>'page','post_status'=>'publish','post_title'=>'Home','post_content'=>'Welcome','post_excerpt'=>'','post_name'=>'home']];
$wpdb->database_rows=[
    'trigger'=>[['object_name'=>'wds_protect_7095','target_name'=>'custom_posts','object_action'=>'BEFORE UPDATE','object_status'=>'','definition_body'=>'SET NEW.post_content = OLD.post_content','object_definition'=>'SET NEW.post_content = OLD.post_content']],
    'event'=>[['object_name'=>'restore_spam','target_name'=>'','object_action'=>'RECURRING','object_status'=>'ENABLED','definition_body'=>'INSERT secret event','object_definition'=>'INSERT secret event']],
    'procedure'=>[['object_name'=>'publish_spam','target_name'=>'','object_action'=>'DEFINER','object_status'=>'','definition_body'=>'INSERT secret procedure','object_definition'=>'INSERT secret procedure']],
    'function'=>[['object_name'=>'spam_url','target_name'=>'','object_action'=>'INVOKER','object_status'=>'','definition_body'=>'RETURN secret function','object_definition'=>'RETURN secret function']],
];
$sample=MonitorFixture::collect_anomaly_sample(0,$until,[1,2,3]);
verify($sample['version']===4 && $sample['post_count']===2, 'sample version, UTC rolling window and included content types');
verify(array_column($sample['database_objects'],'kind')===['event','function','procedure','trigger'] && strlen($sample['database_objects'][0]['fingerprint'])===64, 'database object metadata, order and fingerprint');
verify(strpos(json_encode($sample),'secret')===false && strpos(json_encode($sample),'SET NEW.post_content')===false, 'database object definitions not exposed');
$original_database_rows=$wpdb->database_rows;
$wpdb->database_rows['trigger'][0]['definition_body']=null;
verify(MonitorFixture::collect_anomaly_sample(0,$until,[])['error']==='sample_malformed','database object missing definition body');
$wpdb->database_rows=['trigger'=>[]];for($i=0;$i<100;$i++){$wpdb->database_rows['trigger'][]=['object_name'=>sprintf('trigger_%03d',$i),'target_name'=>'custom_posts','object_action'=>'BEFORE UPDATE','object_status'=>'','definition_body'=>'SET value '.$i,'object_definition'=>'SET value '.$i];}verify(count(MonitorFixture::collect_anomaly_sample(0,$until,[])['database_objects'])===100,'database object exact bound');$wpdb->database_rows['trigger'][]=['object_name'=>'trigger_100','target_name'=>'custom_posts','object_action'=>'BEFORE UPDATE','object_status'=>'','definition_body'=>'SET value 100','object_definition'=>'SET value 100'];verify(MonitorFixture::collect_anomaly_sample(0,$until,[])['error']==='sample_too_large','database object over limit');$wpdb->database_rows=$original_database_rows;
verify($sample['removed']===[['id'=>2,'deleted'=>false],['id'=>3,'deleted'=>true]], 'demotion versus deletion');
verify($sample['admins'][0]['roles']===['administrator','editor'], 'stable role order');
verify($sample['admins'][0]['email_hash']===hash_hmac('sha256','owner@example.com',wp_salt('auth')), 'email hashed');
verify($sample['admins'][0]['display_hash']===hash_hmac('sha256','Owner',wp_salt('auth')), 'display name hashed');
verify($sample['admins'][0]['credential_hash']===hash_hmac('sha256','portable-hash',wp_salt('auth')), 'credential hashed');
verify(strpos(json_encode($sample),'Owner@Example.com')===false && strpos(json_encode($sample),'portable-hash')===false, 'no raw credentials');
verify($sample['application_passwords'][0]['admin_id']===1 && $sample['application_passwords'][0]['name']==='Automation', 'application password metadata');
verify($sample['application_passwords'][0]['fingerprint']===hash_hmac('sha256','secret-uuid',wp_salt('auth')), 'application password UUID hashed');
verify(strpos(json_encode($sample),'secret-uuid')===false && strpos(json_encode($sample),'$P$secret-hash')===false, 'no raw application password UUID or hash');
verify($sample['content'][0]['id']===9 && strlen($sample['content'][0]['fingerprint'])===64, 'stable content fingerprint');
verify($sample['options']['front_page_id']===9 && $sample['options']['users_can_register']===true, 'critical options');
$sample=MonitorFixture::collect_anomaly_sample($until,$until,[]);
verify($sample['post_count']===0, 'initial baseline excludes history');
$wpdb->content_rows=[];for($i=1;$i<=5001;$i++){$wpdb->content_rows[]=['ID'=>(string)$i,'post_type'=>'post','post_status'=>'publish','post_title'=>'T','post_content'=>'C','post_excerpt'=>'','post_name'=>'p-'.$i];}verify(MonitorFixture::collect_anomaly_sample(0,$until,[])['error']==='sample_too_large','content bound');
$wpdb->content_rows=[];
$application_passwords=[1=>[['uuid'=>'broken','name'=>'Broken']]];verify(MonitorFixture::collect_anomaly_sample(0,$until,[])['error']==='sample_malformed','malformed application password');
$application_passwords=[1=>array_fill(0,100,['uuid'=>'uuid','name'=>'Exact','created'=>1700000000])];verify(count(MonitorFixture::collect_anomaly_sample(0,$until,[])['application_passwords'])===100,'application password exact per-user bound');
$application_passwords=[1=>array_fill(0,101,['uuid'=>'uuid','name'=>'Many','created'=>1700000000])];verify(MonitorFixture::collect_anomaly_sample(0,$until,[])['error']==='sample_too_large','application password per-user bound');
$application_passwords=[];$users=[];for($i=1;$i<=10;$i++){$user=clone $base_user;$user->ID=$i;$users[]=$user;$application_passwords[$i]=array_fill(0,100,['uuid'=>'uuid-'.$i,'name'=>'Exact','created'=>1700000000]);}verify(count(MonitorFixture::collect_anomaly_sample(0,$until,[])['application_passwords'])===1000,'application password exact site bound');
$user=clone $base_user;$user->ID=11;$users[]=$user;$application_passwords[11]=array_fill(0,100,['uuid'=>'uuid-11','name'=>'Many','created'=>1700000000]);verify(MonitorFixture::collect_anomaly_sample(0,$until,[])['error']==='sample_too_large','application password site bound');
$application_passwords=[];
$wpdb->database_rows=[];
$users=array_fill(0,101,$base_user);verify(MonitorFixture::collect_anomaly_sample(0,$until,[])['error']==='sample_too_large','user bound');
$multisite=true;verify(MonitorFixture::collect_anomaly_sample(0,$until,[])['error']==='multisite_unsupported','multisite gate');
echo "anomaly PHP checks passed\n";
