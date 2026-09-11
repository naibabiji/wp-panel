<?php
define('ABSPATH', '/fixture/');
define('DAY_IN_SECONDS', 86400);
require __DIR__.'/../../wp-panel-optimizer/includes/trait-anomaly-monitor.php';
class MonitorFixture { use WPP_Optimizer_Anomaly_Monitor_Trait; }
function verify($ok, $message) { if (!$ok) throw new Exception($message); }
try { MonitorFixture::collect_anomaly_sample(0, 100, []); throw new Exception('missing CLI runner gate'); }
catch (RuntimeException $e) { verify($e->getMessage()==='runner_required', 'runner gate'); }
define('WP_PANEL_INVENTORY_RUNNER', true);
$multisite=false;
function is_multisite() { return $GLOBALS['multisite']; }
function wp_salt($scheme) { return 'test-only-site-salt'; }
function get_users($args) {
    verify($args['role']==='administrator' && $args['number']===101, 'bounded administrator query');
    return $GLOBALS['users'];
}
function get_userdata($id) { return $id===2 ? (object)['ID'=>2] : false; }
class SampleDB {
    public $posts='custom_posts'; public $last_error=''; public $params; public $rows=[];
    function prepare($sql, ...$args) {
        verify(strpos($sql, "post_type = 'post'")!==false && strpos($sql,"post_status = 'publish'")!==false, 'ordinary published posts only');
        verify(strpos($sql,'custom_posts')!==false, 'actual table prefix');
        $this->params=$args; return $sql;
    }
    function get_var($sql) {
        $count=0;
        foreach ($this->rows as $row) if ($row[0]==='post' && $row[1]==='publish' && $row[2]>$this->params[0] && $row[2]<=$this->params[1]) $count++;
        return (string)$count;
    }
}
$wpdb=new SampleDB();
$users=[(object)['ID'=>1,'user_login'=>'owner','user_email'=>'Owner@Example.com','roles'=>['editor','administrator']]];
$until=1800000000;
$wpdb->rows=[['post','publish',gmdate('Y-m-d H:i:s',$until-1)], ['post','publish',gmdate('Y-m-d H:i:s',$until-86400)],
    ['post','draft',gmdate('Y-m-d H:i:s',$until-1)], ['product','publish',gmdate('Y-m-d H:i:s',$until-1)],
    ['post','publish',gmdate('Y-m-d H:i:s',$until+1)]];
$sample=MonitorFixture::collect_anomaly_sample(0,$until,[1,2,3]);
verify($sample['post_count']===1, 'UTC rolling window and excluded types/statuses');
verify($sample['removed']===[['id'=>2,'deleted'=>false],['id'=>3,'deleted'=>true]], 'demotion versus deletion');
verify($sample['admins'][0]['roles']===['administrator','editor'], 'stable role order');
verify($sample['admins'][0]['email_hash']===hash_hmac('sha256','owner@example.com',wp_salt('auth')), 'email hashed');
verify(strpos(json_encode($sample),'Owner@Example.com')===false, 'no raw email');
$sample=MonitorFixture::collect_anomaly_sample($until,$until,[]);
verify($sample['post_count']===0, 'initial baseline excludes history');
$users=array_fill(0,101,$users[0]);verify(MonitorFixture::collect_anomaly_sample(0,$until,[])['error']==='sample_failed','user bound');
$multisite=true;verify(MonitorFixture::collect_anomaly_sample(0,$until,[])['error']==='multisite_unsupported','multisite gate');
echo "anomaly PHP checks passed\n";
