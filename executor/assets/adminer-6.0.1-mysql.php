<?php
/** Adminer - Compact database management
* @link https://www.adminer.org/
* @author Jakub Vrana, https://www.vrana.cz/
* @copyright 2007 Jakub Vrana
* @license https://www.apache.org/licenses/LICENSE-2.0 Apache License, Version 2.0
* @license https://www.gnu.org/licenses/gpl-2.0.html GNU General Public License, version 2 (one or other)
* @version 6.0.1
*/namespace
Adminer;const
VERSION="6.0.1";error_reporting(24575);set_error_handler(function($Jc,$Lc){return!!preg_match('~^Undefined (array key|offset|index)~',$Lc);},E_WARNING|E_NOTICE);$od=!preg_match('~^(unsafe_raw)?$~',ini_get("filter.default"));if($od||ini_get("filter.default_flags")){foreach(array('_GET','_POST','_COOKIE','_SERVER')as$X){$rk=filter_input_array(constant("INPUT$X"),FILTER_UNSAFE_RAW);if($rk)$$X=$rk;}}$_COOKIE=array_filter($_COOKIE,'is_scalar');if(function_exists("mb_internal_encoding"))mb_internal_encoding("8bit");function
connection($f=null){return($f?:Db::$instance);}function
adminer(){return
Adminer::$instance;}function
driver(){return
Driver::$instance;}function
connect(){$Gb=adminer()->credentials();$K=Driver::connect($Gb[0],$Gb[1],$Gb[2]);return(is_object($K)?$K:null);}function
idf_unescape($t){if(!preg_match('~^[`\'"[]~',$t))return$t;$ff=substr($t,-1);return
str_replace($ff.$ff,$ff,substr($t,1,-1));}function
q($Q){return
connection()->quote($Q);}function
idx($wa,$w,$j=null){return($wa&&array_key_exists($w,$wa)?$wa[$w]:$j);}function
number($X){return
preg_replace('~[^0-9]+~','',$X);}function
int_type(){return'(tiny|small|medium|big)?int(eger|\d)?';}function
number_type(){return'(^('.int_type().'|decimal|numeric|number|real|(binary_|half_|scaled_)?float\d?|(binary_)?double( precision)?|(small)?money)$)';}function
text_type(){return'char|text'.(JUSH=="sql"?'|enum|set':'');}function
is_searchable(array$l,array$X){if(!isset($l["privileges"]["where"]))return
false;$U=$l["type"];$Bi=$X["val"];$Ma='binary$|bytea|raw|image|bfile|^vector$'.(JUSH=="mssql"?'|^timestamp$':'|^bit').(JUSH=="oracle"?'|^blob|^long|rowid':'');if(preg_match("~$Ma~",$U))return
false;if(preg_match(number_type(),$U)){$D='-?\d+(\.\d+)?';return(bool)preg_match('~^'.$D.(preg_match('~IN$~',$X["op"])?"( *, *$D)*":'').'$~',$Bi);}if(preg_match('~^(small)?date|^timestamp~',$U))return(bool)preg_match('~^\d+-\d+-\d+~',$Bi);if(preg_match('~^time~',$U))return(bool)preg_match('~^\d+:\d+~',$Bi);if(preg_match('~^bool~',$U)||(JUSH=="mssql"&&$U=="bit"))return(bool)preg_match('~^(t|f|true|false|[01])$~i',$Bi);return
true;}function
remove_slashes(array$Kk,$od=false){$K=array();foreach($Kk
as$w=>$X)$K[stripslashes($w)]=(is_array($X)?remove_slashes($X,$od):($od?$X:stripslashes($X)));return$K;}function
bracket_escape($t,$Fa=false){static$bk=array(':'=>':1',']'=>':2','['=>':3','"'=>':4','='=>':5');return
strtr($t,($Fa?array_flip($bk):$bk));}function
url_escape($Q){static$bk=array();if(!$bk){$bk=array(' '=>'+');foreach(str_split("\"'<>#%&+=?".ini_get("arg_separator.input"))as$Wa)$bk[$Wa]=sprintf('%%%02X',ord($Wa));for($r=0;$r<256;$r++){if($r<32||$r>126)$bk[chr($r)]=sprintf('%%%02X',$r);}}return
strtr((string)$Q,$bk);}function
min_version($Nk,$zf="",$f=null){$f=connection($f);$Ki=$f->server_info;if($zf&&preg_match('~([\d.]+)-MariaDB~',$Ki,$A)){$Ki=$A[1];$Nk=$zf;}return$Nk&&version_compare($Ki,$Nk)>=0;}function
charset(Db$e){return(min_version("5.5.3",0,$e)?"utf8mb4":"utf8");}function
ini_set($Og,$Y){return(function_exists('ini_set')?\ini_set($Og,$Y):false);}function
ini_bool($Be){$X=ini_get($Be);return(preg_match('~^(on|true|yes)$~i',$X)||(int)$X);}function
ini_bytes($Be){$X=ini_get($Be);switch(strtolower(substr($X,-1))){case'g':$X=(int)$X*1024;case'm':$X=(int)$X*1024;case'k':$X=(int)$X*1024;}return$X;}function
max_input_vars($L,$bh){$Bf=(int)ini_get("max_input_vars");return($Bf?(int)floor(($Bf-$bh)/$L):0);}function
max_input_vars_error(){$Be="max_input_vars";return
lang(0,"<b>$Be = ".ini_get($Be)."</b>");}function
sid(){static$K;if($K===null)$K=(SID&&!($_COOKIE&&ini_bool("session.use_cookies")));return$K;}function
set_password($Mk,$O,$V,$G){$_SESSION["pwds"][$Mk][$O][$V]=($_COOKIE["adminer_key"]&&is_string($G)?array(encrypt_string($G,$_COOKIE["adminer_key"])):$G);}function
get_password(){$K=get_session("pwds");if(is_array($K))$K=($_COOKIE["adminer_key"]?decrypt_string($K[0],$_COOKIE["adminer_key"]):false);return$K;}function
get_val($I,$l=0,$vb=null){$vb=connection($vb);$J=$vb->query($I);if(!is_object($J))return
false;$L=$J->fetch_row();return($L?$L[$l]:false);}function
get_vals($I,$c=0){$K=array();$J=connection()->query($I);if(is_object($J)){while($L=$J->fetch_row())$K[]=$L[$c];}return$K;}function
get_key_vals($I,$f=null,$Ni=true){$f=connection($f);$K=array();$J=$f->query($I);if(is_object($J)){while($L=$J->fetch_row()){if($Ni)$K[$L[0]]=$L[1];else$K[]=$L[0];}}return$K;}function
get_rows($I,$f=null,$k="<p class='error'>"){$vb=connection($f);$K=array();$J=$vb->query($I);if(is_object($J)){while($L=$J->fetch_assoc())$K[]=$L;}elseif(!$J&&!$f&&$k&&(defined('Adminer\PAGE_HEADER')||$k=="-- "))echo$k.adminer()->error()."\n";return$K;}function
unique_array($L,array$v){foreach($v
as$u){if(preg_match("~^(PRIMARY|UNIQUE)$~",$u["type"])&&!$u["partial"]){$K=array();foreach($u["columns"]as$w){if(!isset($L[$w]))continue
2;$K[$w]=$L[$w];}return$K;}}}function
escape_key($w){if(preg_match('(^([\w(]+)('.str_replace("_",".*",preg_quote(idf_escape("_"))).')([ \w)]+)$)',$w,$A))return$A[1].idf_escape(idf_unescape($A[2])).$A[3];return
idf_escape($w);}function
where(array$Z,array$m=array()){$K=array();foreach((array)$Z["where"]as$w=>$X){$w=bracket_escape($w,true);$c=escape_key($w);$l=idx($m,$w,array());$id=$l["type"];$Ne=$l&&(is_blob($l)||preg_match('~binary~',$id));$K[]=$c.($Ne&&!is_utf8($X)?" = ".driver()->quoteBinary($X):(JUSH=="sql"&&$id=="json"?" = CAST(".q($X)." AS JSON)":(JUSH=="pgsql"&&preg_match('~^jsonb?$~',$l["full_type"])?"::jsonb = ".q($X)."::jsonb":(JUSH=="sql"&&is_numeric($X)&&preg_match('~\.~',$X)?" LIKE ".q($X):(JUSH=="mssql"&&strpos($id,"datetime")===false?" LIKE ".q(preg_replace('~[_%[]~','[\0]',$X)):" = ".unconvert_field($l,q($X)))))));if(JUSH=="sql"&&preg_match('~char|text~',$id)&&preg_match("~[^ -@]~",$X))$K[]="$c = ".q($X)." COLLATE ".charset(connection())."_bin";}foreach((array)$Z["null"]as$w)$K[]=escape_key($w)." IS NULL";return
implode(" AND ",$K);}function
where_columns(array$m){$K=array();foreach((array)$_GET["null"]as$w)$K[$w]=true;foreach((array)$_GET["where"]as$w=>$X){$w=bracket_escape($w,true);foreach($m
as$C=>$l){if($w==$C||strpos($w,idf_escape($C))!==false)$K[$C]=true;}}return$K;}function
where_check($X,array$m=array()){parse_str($X,$Za);remove_slashes(array(&$Za));return
where($Za,$m);}function
where_link($r,$c,$Y,$Lg="="){$Ig=($Y!==null?$Lg:"IS NULL");return"&where[$r][col]=".url_escape($c).($Ig!=first(adminer()->operators())?"&where[$r][op]=".url_escape($Ig):"")."&where[$r][val]=".url_escape($Y);}function
convert_fields(array$d,array$m,array$N=array()){$K="";foreach($d
as$w=>$X){if($N&&!in_array(idf_escape($w),$N))continue;$xa=convert_field($m[$w]);if($xa)$K
.=", $xa AS ".idf_escape($w);}return$K;}function
cookie_path(){return
strtr(preg_replace('~\?.*~','',$_SERVER["REQUEST_URI"]),array(";"=>"%3B",","=>"%2C"));}function
cookie($C,$Y,$pf=2592000){header("Set-Cookie: $C=".rawurlencode($Y).($pf?"; expires=".gmdate("D, d M Y H:i:s",time()+$pf)." GMT":"")."; path=".cookie_path().(HTTPS?"; secure":"").($C=="adminer_import"?"":"; HttpOnly")."; SameSite=lax",false);}function
get_url($zk,$zb){$http_response_header=null;$Kc=array();set_error_handler(function($Jc,$k)use(&$Kc){$Kc[]=preg_replace('~^file_get_contents\([^)]*\):\s*~','',$k);return
true;});$K=file_get_contents($zk,false,$zb);restore_error_handler();$ae=(function_exists('http_get_last_response_headers')?http_get_last_response_headers():$http_response_header);return
array($K,(preg_match('~^HTTP/[\d.]+ (\d+)~',idx($ae,0,''),$A)?$A[1]:''),(array)$ae,($K===false?implode("\n",$Kc):''),);}function
get_settings($Bb){parse_str($_COOKIE[$Bb],$Oi);return$Oi;}function
get_setting($w,$Bb="adminer_settings",$j=null){return
idx(get_settings($Bb),$w,$j);}function
save_settings(array$Oi,$Bb="adminer_settings"){$Y=http_build_query($Oi+get_settings($Bb));cookie($Bb,$Y);$_COOKIE[$Bb]=$Y;}function
restart_session(){if(!ini_bool("session.use_cookies")&&(!function_exists('session_status')||session_status()==PHP_SESSION_NONE))session_start();}function
stop_session($ud=false){$Bk=ini_bool("session.use_cookies");if(!$Bk||$ud){session_write_close();if($Bk&&ini_set("session.use_cookies",'0')===false)session_start();}}function&get_session($w){return$_SESSION[$w][DRIVER][SERVER][$_GET["username"]];}function
set_session($w,$X){$_SESSION[$w][DRIVER][SERVER][$_GET["username"]]=$X;}function
auth_url($Mk,$O,$V,$i=null){$yk=remove_from_uri(implode("|",array_keys(SqlDriver::$drivers))."|username|ext|".($i!==null?"db|":"").($Mk=='mssql'||$Mk=='pgsql'?"":"ns|").session_name());preg_match('~([^?]*)\??(.*)~',$yk,$A);return"$A[1]?".(sid()?SID."&":"").($_GET["ext"]?"ext=".url_escape($_GET["ext"])."&":"").($Mk!="server"||$O!=""?url_escape($Mk)."=".url_escape($O)."&":"")."username=".url_escape($V).($i!=""?"&db=".url_escape($i):"").($A[2]?"&$A[2]":"");}function
is_ajax(){return($_SERVER["HTTP_X_REQUESTED_WITH"]=="XMLHttpRequest");}function
redirect($_,$B=null){if($B!==null){restart_session();$_SESSION["messages"][preg_replace('~^[^?]*~','',($_!==null?$_:$_SERVER["REQUEST_URI"]))][]=$B;}if($_!==null){if($_=="")$_=".";header("Location: $_");exit;}}function
query_redirect($I,$_,$B,$bi=true,$Sc=true,$dd=false,$Oj=""){if($Sc){$fj=microtime(true);$dd=!connection()->query($I);$Oj=format_time($fj);}$Zi=($I?adminer()->messageQuery($I,$Oj,$dd):"");if($dd){adminer()->error
.=adminer()->error().$Zi.script("messagesPrint();")."<br>";return
false;}if($bi)redirect($_,$B.$Zi);return
true;}class
Queries{static$queries=array();static$start=0;}function
queries($I){if(!Queries::$start)Queries::$start=microtime(true);Queries::$queries[]=(driver()->delimiter!=';'?$I:(preg_match('~;$~',$I)?"DELIMITER ;;\n$I;\nDELIMITER ":$I).";");return
connection()->query($I);}function
apply_queries($I,array$T,$Mc='Adminer\table'){foreach($T
as$R){if(!queries("$I ".$Mc($R)))return
false;}return
true;}function
queries_redirect($_,$B,$bi){$Wh=implode("\n",Queries::$queries);$Oj=format_time(Queries::$start);return
query_redirect($Wh,$_,$B,$bi,false,!$bi,$Oj);}function
format_time($fj){return
lang(1,max(0,microtime(true)-$fj));}function
relative_uri($yk=''){return
preg_replace_callback('~^[^?]*~',function($A){return
str_replace(":","%3A",$A[0]);},preg_replace('~^[^?]*/([^?]*)~','\1',($yk?:$_SERVER["REQUEST_URI"])));}function
remove_from_uri($gh=""){return
substr(preg_replace("~(?<=[?&])($gh".(SID?"":"|".session_name()).")=[^&]*&~",'',relative_uri()."&"),0,-1);}function
get_files($C,$Ub=false){$kd=$_FILES[$C];if(!$kd)return
null;foreach($kd
as$w=>$X)$kd[$w]=(array)$X;$K=array();foreach($kd["error"]as$w=>$k){if($k)return$k;$n=$kd["name"][$w];$Wj=$kd["tmp_name"][$w];$xb=file_get_contents($Ub&&preg_match('~\.gz$~',$n)?"compress.zlib://$Wj":$Wj);if($Ub){$fj=substr($xb,0,3);if(function_exists("iconv")&&preg_match("~^\xFE\xFF|^\xFF\xFE~",$fj))$xb=iconv("utf-16","utf-8",$xb);elseif($fj=="\xEF\xBB\xBF")$xb=substr($xb,3);}$K[]=array($n,$xb);}return$K;}function
get_file($w,$Ub=false,$ac=""){$nd=get_files($w,$Ub);if(!is_array($nd))return$nd;$K='';foreach($nd
as$kd){$xb=$kd[1];$K
.=$xb;if($ac)$K
.=(preg_match("($ac\\s*\$)",$xb)?"":$ac)."\n\n";}return$K;}function
upload_error($k){$Jf=($k==UPLOAD_ERR_INI_SIZE?ini_get("upload_max_filesize"):0);return($k?lang(2).($Jf?" ".lang(3,$Jf):""):lang(4));}function
is_utf8($X){return(preg_match('~~u',$X)&&!preg_match('~[\0-\x8\xB\xC\xE-\x1F]~',$X));}function
format_number($X){return
strtr(number_format($X,0,".",lang(5)),preg_split('~~u',lang(6),-1,PREG_SPLIT_NO_EMPTY));}function
format_status(array$S,$w){$X=idx($S,$w,'?');if(!is_numeric($X))return
h($X);if($X<0)return'?';$ta=($w=="Rows"&&(JUSH=="sqlite"||$S["Engine"]==(JUSH=="pgsql"?"table":"InnoDB")));return($ta?"~ ":"").format_number($X);}function
friendly_url($X){return
preg_replace('~\W~i','-',$X);}function
table_status1($R,$ed=false){$K=table_status($R,$ed);return($K?reset($K):array("Name"=>$R));}function
column_foreign_keys($R){$K=array();foreach(adminer()->foreignKeys($R)as$o){foreach($o["source"]as$X)$K[$X][]=$o;}return$K;}function
fields_from_edit(){$K=array();foreach((array)$_POST["field_keys"]as$w=>$X){if($X!=""){$X=bracket_escape($X);$_POST["function"][$X]=$_POST["field_funs"][$w];$_POST["fields"][$X]=$_POST["field_vals"][$w];}}foreach((array)$_POST["fields"]as$w=>$X){$C=bracket_escape($w,true);$K[$C]=array("field"=>$C,"full_type"=>"","type"=>"","privileges"=>array("insert"=>1,"update"=>1,"where"=>1,"order"=>1),"null"=>true,"auto_increment"=>($C==driver()->primary),);}return$K;}function
dump_headers($me,$gg=false){$K=adminer()->dumpHeaders($me,$gg);$dh=$_POST["output"];if($dh!="text"||$K=="tar"){$sb=($dh!="text"&&$dh!="file"&&preg_match('~^[0-9a-z]+$~',$dh)?".$dh":"");header("Content-Disposition: attachment; filename=".adminer()->dumpFilename($me).".$K$sb");}session_write_close();if(!ob_get_level())ob_start(null,4096);ob_flush();flush();return$K;}function
dump_csv(array$L){$jk=$_POST["format"]=="tsv";foreach($L
as$w=>$X){if(preg_match('~["\n]|^0[^.]|\.\d*0$|'.($jk?'\t':'[,;]|^$').'~',$X))$L[$w]='"'.str_replace('"','""',$X).'"';}echo
implode(($_POST["format"]=="csv"?",":($jk?"\t":";")),$L)."\r\n";}function
parse_csv($Jb,$Ji){$K=array();preg_match_all('~(?>"[^"]*"|[^"\r\n]+)+~',$Jb,$_f);foreach($_f[0]as$L){preg_match_all("~((?>\"[^\"]*\")+|[^$Ji]*)$Ji~",$L.$Ji,$Af);$K[]=$Af[1];}return$K;}function
csv_value($X){return(preg_match('~^".*"$~s',$X)?str_replace('""','"',substr($X,1,-1)):$X);}function
apply_sql_function($q,$c){return($q?($q=="unixepoch"?"DATETIME($c, '$q')":($q=="count distinct"?"COUNT(DISTINCT ":strtoupper("$q("))."$c)"):$c);}function
get_temp_dir(){return
ini_get("upload_tmp_dir")?:sys_get_temp_dir();}function
file_open_lock($n){if(is_link($n))return;$p=@fopen($n,"c+");if(!$p)return;@chmod($n,0660);if(!flock($p,LOCK_EX)){fclose($p);return;}return$p;}function
file_write_unlock($p,$Nb){rewind($p);fwrite($p,$Nb);ftruncate($p,strlen($Nb));file_unlock($p);}function
file_unlock($p){flock($p,LOCK_UN);fclose($p);}function
first(array$wa){return
reset($wa);}function
password_file($g){$n=get_temp_dir()."/adminer.key";if(!$g&&!file_exists($n))return'';$p=file_open_lock($n);if(!$p)return'';$K=stream_get_contents($p);if(!$K){$K=rand_string();file_write_unlock($p,$K);}else
file_unlock($p);return$K;}function
rand_string(){return(function_exists('random_bytes')?bin2hex(random_bytes(16)):md5(uniqid(strval(mt_rand()),true)));}function
select_value($X,$z,array$l,$Mj){if(is_array($X)){$K="";if(array_filter($X,'is_array')==array_values($X)){$Ye=array();foreach($X
as$W)$Ye+=array_fill_keys(array_keys($W),null);foreach(array_keys($Ye)as$Xe)$K
.="<th>".h($Xe);foreach($X
as$W){$K
.="<tr>";foreach(array_merge($Ye,$W)as$Gk)$K
.="<td>".select_value($Gk,$z,$l,$Mj);}}else{foreach($X
as$Xe=>$W)$K
.="<tr>".($X!=array_values($X)?"<th>".h($Xe):"")."<td>".select_value($W,$z,$l,$Mj);}return"<table>$K</table>";}if(!$z)$z=adminer()->selectLink($X,$l);if($z===null){if(is_mail($X))$z="mailto:$X";if(is_url($X))$z=$X;}$X=driver()->value($X,$l);$K=adminer()->editVal($X,$l);if($K!==null){if(!is_utf8($K))$K="\0";elseif($Mj!=""&&is_shortable($l))$K=shorten_utf8($K,max(0,+$Mj));else$K=h($K);}return
adminer()->selectVal($K,$z,$l,$X);}function
is_blob(array$l){return
preg_match('~blob|bytea|raw|file'.(JUSH=="mssql"?'|binary|image':'').'~',$l["type"])&&!in_array($l["type"],idx(driver()->structuredTypes(),lang(7),array()));}function
is_mail($Ac){$za='[-a-z0-9!#$%&\'*+/=?^_`{|}~]';$qc='[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])';$wh="$za+(\\.$za+)*@($qc?\\.)+$qc";return
is_string($Ac)&&preg_match("(^$wh(,\\s*$wh)*\$)i",$Ac);}function
is_url($Q){$qc='[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])';return
preg_match("~^((https?):)?//($qc?\\.)+$qc(:\\d+)?(/.*)?(\\?.*)?(#.*)?\$~i",$Q);}function
is_shortable(array$l){return!preg_match('~'.number_type().'|date|time|year~',$l["type"]);}function
host_port($O){return(preg_match('~^(:([^:].*)|(\[(.+)\]|(([^:]+://)?[^:]+))(:(\d+))?)$~',$O,$A)?array($A[4].$A[5],$A[2].$A[8]):array($O,''));}function
count_rows($R,array$Z,$Oe,array$Kd){$I=" FROM ".table($R).($Z?" WHERE ".implode(" AND ",$Z):"");return($Oe&&(JUSH=="sql"||count($Kd)==1)?"SELECT COUNT(DISTINCT ".implode(", ",$Kd).")$I":"SELECT COUNT(*)".($Oe?" FROM (SELECT 1$I GROUP BY ".implode(", ",$Kd).") x":$I));}function
slow_query($I){$i=adminer()->database();$Pj=adminer()->queryTimeout();$Si=driver()->slowQuery($I,$Pj);$f=null;if(!$Si&&support("kill")){$f=connect();if($f&&($i==""||$f->select_db($i))){$Ze=get_val(connection_id(),0,$f);echo
script("const timeout = setTimeout(() => { ajax('".js_escape(ME)."script=kill', function () {}, 'kill=$Ze&token=".get_token()."'); }, 1000 * $Pj);");}}ob_flush();flush();$K=@get_key_vals(($Si?:$I),$f,false);if($f){echo
script("clearTimeout(timeout);");ob_flush();flush();}return$K;}function
get_token(){$Zh=rand(1,1e6);return($Zh^$_SESSION["token"]).":$Zh";}function
verify_token(){list($Xj,$Zh)=explode(":",$_POST["token"]);return($Zh^$_SESSION["token"])==$Xj&&in_array($_SERVER["HTTP_SEC_FETCH_SITE"],array("","same-origin"));}function
compress_alphabet(){return
strtr(implode(range('"','~')),"'\\","!\n");}function
decompress_string($Q,$gc=""){$qa=array_flip(str_split(compress_alphabet()));$x=strlen($Q);$Ik=($x?13*($x-1)/2-$qa[$Q[0]]:0);$Ma="";$ni=0;$oi=0;for($r=1;$r<$x;$r+=2){$ni=($ni<<13)+$qa[$Q[$r]]*93+$qa[$Q[$r+1]];$oi+=13;while($oi>=8&&$Ik>=8){$oi-=8;$Ik-=8;$Ma
.=chr($ni>>$oi);$ni&=(1<<$oi)-1;}}if($Ma=="")return"";if($gc!=""&&function_exists('inflate_init'))return
inflate_add(inflate_init(ZLIB_ENCODING_RAW,array('dictionary'=>$gc)),$Ma,ZLIB_FINISH);return($gc==""&&function_exists('gzinflate')?gzinflate($Ma):inflate($Ma,$gc));}function
inflate($Ma,$gc=""){$mf=array(3,4,5,6,7,8,9,10,11,13,15,17,19,23,27,31,35,43,51,59,67,83,99,115,131,163,195,227,258);$nf=array(0,0,0,0,0,0,0,0,1,1,1,1,2,2,2,2,3,3,3,3,4,4,4,4,5,5,5,5,0);$kc=array(1,2,3,4,5,7,9,13,17,25,33,49,65,97,129,193,257,385,513,769,1025,1537,2049,3073,4097,6145,8193,12289,16385,24577);$mc=array(0,0,0,0,1,1,2,2,3,3,4,4,5,5,6,6,7,7,8,8,9,9,10,10,11,11,12,12,13,13);$K=$gc;$H=0;do{$pd=inflate_bits($Ma,$H,1);$U=inflate_bits($Ma,$H,2);if(!$U){$H=($H+7)&~7;$x=inflate_bits($Ma,$H,16);$H+=16;$K
.=substr($Ma,$H>>3,$x);$H+=$x<<3;}else{if($U==1){$uf=array_merge(array_fill(0,144,8),array_fill(0,112,9),array_fill(0,24,7),array_fill(0,8,8));$nc=array_fill(0,30,5);}else{$tf=inflate_bits($Ma,$H,5)+257;$lc=inflate_bits($Ma,$H,5)+1;$E=array(16,17,18,0,8,7,9,6,10,5,11,4,12,3,13,2,14,1,15);$Wf=array_fill(0,19,0);$Vf=inflate_bits($Ma,$H,4)+4;for($r=0;$r<$Vf;$r++)$Wf[$E[$r]]=inflate_bits($Ma,$H,3);$Xf=inflate_table($Wf);$of=array();while(count($of)<$tf+$lc){$rj=inflate_symbol($Ma,$H,$Xf);if($rj==16)$of=array_merge($of,array_fill(0,inflate_bits($Ma,$H,2)+3,end($of)));elseif($rj==17)$of=array_merge($of,array_fill(0,inflate_bits($Ma,$H,3)+3,0));elseif($rj==18)$of=array_merge($of,array_fill(0,inflate_bits($Ma,$H,7)+11,0));else$of[]=$rj;}$uf=array_slice($of,0,$tf);$nc=array_slice($of,$tf);}$vf=inflate_table($uf);$pc=inflate_table($nc);while(($rj=inflate_symbol($Ma,$H,$vf))!=256){if($rj<256)$K
.=chr($rj);else{$x=$mf[$rj-257]+inflate_bits($Ma,$H,$nf[$rj-257]);$oc=inflate_symbol($Ma,$H,$pc);$Ag=strlen($K)-$kc[$oc]-inflate_bits($Ma,$H,$mc[$oc]);for($r=0;$r<$x;$r++)$K
.=$K[$Ag+$r];}}}}while(!$pd);return($gc==""?$K:substr($K,strlen($gc)));}function
inflate_bits($Ma,&$H,$Db){$K=0;for($r=0;$r<$Db;$r++){$K+=((ord($Ma[$H>>3])>>($H&7))&1)<<$r;$H++;}return$K;}function
inflate_table(array$of){$R=array();$hb=0;for($Na=1;$Na<=max($of);$Na++){foreach($of
as$rj=>$x){if($x==$Na){$R[$Na][$hb]=$rj;$hb++;}}$hb<<=1;}return$R;}function
inflate_symbol($Ma,&$H,array$R){$hb=0;$Na=0;do{$hb=($hb<<1)+inflate_bits($Ma,$H,1);$Na++;}while(!isset($R[$Na][$hb]));return$R[$Na][$hb];}function
script($Wi,$ak="\n"){return"<script".nonce().">$Wi</script>$ak";}function
script_src($zk,$Xb=false){return"<script src='".h($zk)."'".nonce().($Xb?" defer":"")."></script>\n";}function
nonce(){return' nonce="'.get_nonce().'"';}function
on($Nc,$Sd,$ua=null){$va=array();foreach(array_slice(func_get_args(),2)as$X)$va[]=json_encode($X,256);return" data-on$Nc='".str_replace(array('&','<',"'"),array('&amp;','&lt;','&#039;'),"$Sd(".implode(", ",$va).")")."'";}function
input_hidden($C,$Y=""){return"<input type='hidden' name='".h($C)."' value='".h($Y)."'>\n";}function
input_token(){return
input_hidden("token",get_token());}function
target_blank(){return' target="_blank" rel="noreferrer noopener"';}function
h($Q){return
str_replace(array('&','<','"',"'","\0"),array('&amp;','&lt;','&quot;','&#039;','&#0;'),$Q);}function
nl_br($Q){return
str_replace("\n","<br>",$Q);}function
checkbox($C,$Y,$bb,$bf="",$b="",$gb="",$df=""){$K="<input type='checkbox' name='$C' value='".h($Y)."'".($bb?" checked":"").($bf==""&&$gb?" class='$gb'":"").($df?" aria-labelledby='$df'":"").$b.">";return($bf!=""?"<label".($gb?" class='$gb'":"").">$K".h($bf)."</label>":$K);}function
optionlist($Pg,$Gi=null,$Ck=false){$K="";foreach($Pg
as$Xe=>$W){$Qg=array($Xe=>$W);if(is_array($W)){$K
.='<optgroup label="'.h($Xe).'">';$Qg=$W;}foreach($Qg
as$w=>$X)$K
.='<option'.($Ck||is_string($w)?' value="'.h($w).'"':'').($Gi!==null&&($Ck||is_string($w)?(string)$w:$X)===$Gi?' selected':'').'>'.h($X);if(is_array($W))$K
.='</optgroup>';}return$K;}function
html_select($C,array$Pg,$Y="",$b="",$df=""){static$bf=0;$cf="";if(!$df&&substr($Pg[""],0,1)=="("){$bf++;$df="label-$bf";$cf="<option value='' id='$df'>".h($Pg[""]);unset($Pg[""]);}return"<select name='".h($C)."'".($df?" aria-labelledby='$df'":"")."$b>".$cf.optionlist($Pg,$Y)."</select>";}function
html_radios($C,array$Pg,$Y="",$Ji=""){$K="";foreach($Pg
as$w=>$X)$K
.="<label><input type='radio' name='".h($C)."' value='".h($w)."'".($w==$Y?" checked":"").">".h($X)."</label>$Ji";return$K;}function
confirm($B=""){return
on('click','confirmClick',$B?:lang(8));}function
print_fieldset($s,$lf,$Qk=false){echo"<fieldset><legend>","<a href='#fieldset-$s' class='toggle'>$lf</a>","</legend>","<div id='fieldset-$s'".($Qk?"":" class='hidden'").">\n";}function
bold($Pa,$gb=""){return($Pa?" class='active $gb'":($gb?" class='$gb'":""));}function
js_escape($Q){return
str_replace("<","\\x3C",addcslashes($Q,"\r\n'\\"));}function
js_escape_re($Q){return
addcslashes(preg_quote($Q,"/"),"\r\n");}function
pagination_href($F){return
remove_from_uri("page|next").($F?"&page=$F".($_GET["next"]!=""?"&next=".url_escape($_GET["next"]):""):"");}function
pagination($F,$Kb){return" ".($F==$Kb?($F?"<b>".($F+1)."</b>":$F+1):'<a href="'.h(pagination_href($F)).'">'.($F+1)."</a>");}function
hidden_fields(array$Sh,array$pe=array(),$Kh=''){$K=false;foreach($Sh
as$w=>$X){if(!in_array($w,$pe)){if(is_array($X))hidden_fields($X,array(),$w);else{$K=true;echo
input_hidden(($Kh?$Kh."[$w]":$w),$X);}}}return$K;}function
hidden_fields_get(){echo(sid()?input_hidden(session_name(),session_id()):''),($_GET["ext"]?input_hidden("ext",$_GET["ext"]):""),(isset($_GET[DRIVER])?input_hidden(DRIVER,SERVER):""),input_hidden("username",$_GET["username"]);}function
on_upload_progress(&$xk){$xk=(ini_bool("session.upload_progress.enabled")&&ini_get("session.upload_progress.name")?rand_string():"");return($xk?on('submit','uploadProgress',ME."upload=$xk",SESSION_NAME."=$xk"):"");}function
file_input($b,$ni=""){$Df="max_file_uploads";$Ef=ini_get($Df);$Jf="upload_max_filesize";$Kf=ini_bytes($Jf);$Hh=ini_bytes("post_max_size");if($Hh&&$Hh<$Kf){$Jf="post_max_size";$Kf=$Hh;}$Lf=ini_get($Jf);return(ini_bool("file_uploads")?"<input type='file'$b".on('change','fileChange',(int)$Ef,lang(9,"$Df = $Ef"),$Kf,lang(9,"$Jf = $Lf")).">$ni":lang(10));}function
enum_input($U,$b,array$l,$Y,$Dc=""){preg_match_all("~'((?:[^']|'')*)'~",$l["length"],$_f);$Kh=($l["type"]=="enum"?"val-":"");$bb=(is_array($Y)?in_array("null",$Y):$Y===null);$K=($l["null"]&&$Kh?"<label><input type='$U'$b value='null'".($bb?" checked":"")."><i>$Dc</i></label>":"");foreach($_f[1]as$X){$X=stripcslashes(str_replace("''","'",$X));$bb=(is_array($Y)?in_array($Kh.$X,$Y):$Y===$X);$K
.=" <label><input type='$U'$b value='".h($Kh.$X)."'".($bb?' checked':'').'>'.h(adminer()->editVal($X,$l)).'</label>';}return$K;}function
input(array$l,$Y,$q,$Da=false,$vk=false){$C=h(bracket_escape($l["field"]));echo"<td class='function'>";if(is_array($Y)&&!$q)$q="json";$Ve=($q=="json"||preg_match('~^jsonb?$~',$l["full_type"]));if($Ve&&$Y!=''&&(JUSH!="pgsql"||$l["type"]!="json")&&(is_array($Y)||!$_POST["save"]))$Y=json_encode(is_array($Y)?$Y:json_decode($Y),128|64|256);$mi=(JUSH=="mssql"&&$vk&&$l["auto_increment"]);if($mi&&!$_POST["save"])$q=null;$Ed=(isset($_GET["select"])||$mi?array("orig"=>lang(11)):array())+adminer()->editFunctions($l);$Ic=driver()->enumLength($l);if($Ic){$l["type"]="enum";$l["length"]=$Ic;}$b=" name='fields[$C]".($l["type"]=="enum"||$l["type"]=="set"?"[]":"")."'".($Da?" autofocus":"");echo
driver()->unconvertFunction($l)." ";$R=$_GET["edit"]?:$_GET["select"];if($l["type"]=="enum")echo
h($Ed[""])."<td>".adminer()->editInput($R,$l,$b,$Y);else{$Ud=(in_array($q,$Ed)||isset($Ed[$q]));$qd=0;foreach($Ed
as$w=>$X){if($w===""||!$X)break;$qd++;}echo(count($Ed)>1?"<select name='function[$C]'".on('change','functionChange').on_help_value('^SQL$').">".optionlist($Ed,$q===null||$Ud?$q:"")."</select>":h(reset($Ed)))."<td".($qd&&count($Ed)>1?on('input','skipOriginal',$qd):"").">";$De=adminer()->editInput($R,$l,$b,$Y);if($De!="")echo$De;elseif(preg_match('~bool~',$l["type"]))echo"<input type='hidden'$b value='0'>"."<input type='checkbox'".(preg_match('~^(1|t|true|y|yes|on)$~i',$Y)?" checked":"")."$b value='1'>";elseif($l["type"]=="set")echo
enum_input("checkbox",$b,$l,(is_string($Y)?explode(",",$Y):$Y));elseif(is_blob($l)&&ini_bool("file_uploads"))echo"<input type='file' name='fields-$C'>";elseif($Ve)echo"<textarea$b cols='50' rows='12' class='jush-json'>".h($Y).'</textarea>';elseif(($Lj=preg_match('~text|lob|memo~i',$l["type"]))||preg_match("~\n~",$Y)){if($Lj&&JUSH!="sqlite")$b
.=" cols='50' rows='12'";else{$M=min(12,substr_count($Y,"\n")+1);$b
.=" cols='30' rows='$M'";}echo"<textarea$b>".h($Y).'</textarea>';}else{$mk=driver()->types();$Mf=(!preg_match('~int~',$l["type"])&&preg_match('~^(\d+)(,(\d+))?$~',$l["length"],$A)?((preg_match("~binary~",$l["type"])?2:1)*$A[1]+($A[3]?1:0)+($A[2]&&!$l["unsigned"]?1:0)):($mk[$l["type"]]?$mk[$l["type"]]+($l["unsigned"]?0:1):0));if(JUSH=='sql'&&min_version(5.6)&&preg_match('~time~',$l["type"]))$Mf+=7;echo"<input".((!$Ud||$q==="")&&preg_match('~^'.int_type().'$~',$l["type"])&&!preg_match('~\[]~',$l["full_type"])?" type='number'":"")." value='".h($Y)."'".($Mf?" data-maxlength='$Mf'":"").(preg_match('~char|binary~',$l["type"])&&$Mf>20?" size='".($Mf>99?60:40)."'":"")."$b>";}echo
adminer()->editHint($R,$l,$Y),(count($Ed)>1?script("fire(qs('select', qsl('td').previousSibling), 'change');",""):"");}}function
process_input(array$l){$t=bracket_escape($l["field"]);$q=idx($_POST["function"],$t);if($q=="orig")return(preg_match('~^CURRENT_TIMESTAMP~i',$l["on_update"])?idf_escape($l["field"]):false);if($q=="NULL")return"NULL";if(is_blob($l)&&ini_bool("file_uploads")){$kd=get_file("fields-$t");if(!is_string($kd))return
false;return
driver()->quoteBinary($kd);}$Y=idx($_POST["fields"],$t);if($Y===null)return
false;if($l["type"]=="enum"||driver()->enumLength($l)){$Y=idx($Y,0);if($Y=="orig"||!$Y)return
false;if($Y=="null")return"NULL";$Y=substr($Y,4);}if($l["auto_increment"]&&$Y=="")return
null;if($l["type"]=="set")$Y=implode(",",(array)$Y);if($q=="json"){$Y=json_decode($Y,true);if(!is_array($Y))return
false;return$Y;}return
adminer()->processInput($l,$Y,$q);}function
search_tables(){$_GET["where"][0]["val"]=$_POST["query"];$Ii="<ul>\n";foreach(table_status('',true)as$R=>$S){$C=adminer()->tableName($S);if(isset($S["Engine"])&&$C!=""&&(!$_POST["tables"]||in_array($R,$_POST["tables"]))){$J=connection()->query("SELECT".limit("1 FROM ".table($R)," WHERE ".implode(" AND ",adminer()->selectSearchProcess(fields($R),array())),1));if(!$J||$J->fetch_row()){$Oh="<a href='".h(ME."select=".url_escape($R)."&where[0][op]=".url_escape($_GET["where"][0]["op"])."&where[0][val]=".url_escape($_GET["where"][0]["val"]))."'>$C</a>";echo"$Ii<li>".($J?$Oh:"<p class='error'>$Oh: ".adminer()->error())."\n";$Ii="";}}}echo($Ii?"<p class='message'>".lang(12):"</ul>")."\n";}function
on_help($Lj,$Qi=0){return
on('mouseover','helpMouseover',$Lj,$Qi).on('mouseout','helpMouseout');}function
on_help_value($ii="",$li=""){return
on('mouseover','helpValueMouseover',$ii,$li).on('mouseout','helpMouseout');}function
edit_form($R,array$m,$L,$vk,$k='',$I='',$Oj=''){$vj=adminer()->tableName(table_status1($R,true));page_header(($vk?lang(13):lang(14)),$k,array("select"=>array($R,$vj)),$vj);adminer()->editRowPrint($R,$m,$L,$vk,$I,$Oj);if($L===false){echo"<p class='error'>".lang(15)."\n";return;}echo"<form action='' method='post' enctype='multipart/form-data' id='form'>\n";$zc=false;$Wk=($vk&&!isset($_GET["select"])?where_columns($m):array());$_b=(count($Wk)!=count($m));if(!$_b)$Wk=array();if(!$m)echo"<p class='error'>".lang(16)."\n";else{echo"<table class='layout nowrap'".on('keydown','editingKeydown').">\n";$Da=!$_POST;foreach($m
as$C=>$l){echo"<tr".($Wk[$C]?on('change','whereChange'):"")."><th>".adminer()->fieldName($l);$j=idx($_GET["set"],bracket_escape($C));if($j===null){$j=$l["default"];if($l["type"]=="bit"&&preg_match("~^b'([01]*)'\$~",$j,$ji))$j=$ji[1];if(JUSH=="sql"&&preg_match('~binary~',$l["type"]))$j=bin2hex($j);}$Y=($L!==null?($L[$C]!=""&&JUSH=="sql"&&preg_match("~enum|set~",$l["type"])&&is_array($L[$C])?implode(",",$L[$C]):(is_bool($L[$C])?+$L[$C]:$L[$C])):(!$vk&&$l["auto_increment"]?"":(isset($_GET["select"])?false:$j)));if(!$_POST["save"]&&is_string($Y))$Y=adminer()->editVal($Y,$l);if(($vk&&!isset($l["privileges"]["update"]))||$l["generated"])echo"<td class='function'><td>".select_value($Y,'',$l,null);else{$zc=true;$q=($_POST["save"]?idx($_POST["function"],bracket_escape($C),""):($vk&&preg_match('~^CURRENT_TIMESTAMP~i',$l["on_update"])?"now":($Y===false?null:($Y!==null?'':'NULL'))));if(!$_POST&&!$vk&&$Y==$l["default"]&&preg_match('~^[\w.]+\(~',$Y))$q="SQL";if(preg_match("~time~",$l["type"])&&preg_match('~^CURRENT_TIMESTAMP~i',$Y)){$Y="";$q="now";}if($l["type"]=="uuid"&&$Y=="uuid()"){$Y="";$q="uuid";}if($Da!==false)$Da=($l["auto_increment"]||$q=="now"||$q=="uuid"?null:true);input($l,$Y,$q,$Da,$vk);if($Da)$Da=false;}}if(!fields($R)&&driver()->primary!="")echo"<tr>"."<th><input name='field_keys[]'".on('input','fieldChange').">"."<td class='function'>".html_select("field_funs[]",adminer()->editFunctions(array("null"=>isset($_GET["select"]))))."<td><input name='field_vals[]'>";echo"</table>\n";}echo"<p>\n";if($zc){echo"<input type='submit' value='".lang(17)."'>\n";if(!isset($_GET["select"])&&$_b){$hc=($Wk&&($k!=""||adminer()->error!="")?" disabled":"");echo"<input type='submit' name='insert' value='".($vk?lang(18):lang(19))."' title='Ctrl+Shift+Enter'$hc".($vk?on('click','ajaxForm',lang(20)):"").">\n";}}echo($vk?"<input type='submit' name='delete' value='".lang(21)."'".confirm().">\n":"");if(isset($_GET["select"]))hidden_fields(array("check"=>(array)$_POST["check"],"clone"=>$_POST["clone"],"all"=>$_POST["all"]));echo
input_hidden("referer",(isset($_POST["referer"])?$_POST["referer"]:$_SERVER["HTTP_REFERER"])),input_hidden("save",1),input_token(),"</form>\n";}function
repeat_pattern($wh,$x){return
str_repeat("$wh{0,65535}",$x/65535)."$wh{0,".($x%65535)."}";}function
shorten_utf8($Q,$x=80,$nj=""){if(!preg_match("(^(".repeat_pattern("[\t\r\n -\x{10FFFF}]",$x).")($)?)u",$Q,$A))preg_match("(^(".repeat_pattern("[\t\r\n -~]",$x).")($)?)",$Q,$A);return
h($A[1]).$nj.(isset($A[2])?"":"<i>…</i>");}function
icon($le,$C,$ke,$Rj,$b=""){return"<button ".($C?"type='submit' name='$C'":"draggable='true' tabindex='-1'")." title='".h($Rj)."' class='icon icon-$le".($C?"":" jsonly")."'$b><span>$ke</span></button>";}function
copy_icon(){$Cb=lang(22);return"<a href='' class='jsonly icon-copy' title='$Cb'><span>$Cb</span></a>";}if(isset($_GET["file"])){if(substr(VERSION,-4)!='-dev'){if($_SERVER["HTTP_IF_MODIFIED_SINCE"]){header("HTTP/1.1 304 Not Modified");exit;}header("Expires: ".gmdate("D, d M Y H:i:s",time()+365*24*60*60)." GMT");header("Last-Modified: ".gmdate("D, d M Y H:i:s")." GMT");header("Cache-Control: immutable");}ini_set("zlib.output_compression",'1');if($_GET["file"]=="default.css"){header("Content-Type: text/css; charset=utf-8");echo
decompress_string('*c0=@iDWB2P?H*{U)^:;B/4!N2Ch9&hJv;rrHHN,,V&KA"nRfwb9E:tfItOm[T$"DXBX~p!.VU_tTHo)6Y?9q/$mNiohTvI>+a<Y{uWk}`:y,3U4,>E(&Rg+L!L
o2PEgsnloQe<:k0oib.Mj<,:^!s
zL
u)CIc01D]MByKv5]ERUpyZdlppD_9oR;P9hVvq0j@^d:4.VmF0)NWe(|3H6L6o0Ws>bwAO`m9rG2Wt;hhRg!_Vf4_nB|@W/dk68Q<R1B`1Dm8:;z,2
U)U-,adrWa=3ExJ-QhN)_F%%ndS%Ly!><d61_"qU8+_TBHX@<(PwklcY!u8hbgk-
[;Rh`$j<M_/v1L?D1XOE!*3"aaCtLgr:&VCx-w"#!BYQEeE<?Ub!+aqVSz#me-7jAZ&6o[("Z/yYlM,,wx$LBFo4W,*sEWn"i_Dff8!3g>THyt3FDVLmGrq+phsrc9%K8ON1DXS`8$MRiPoh2TUidX#(7Q"{]y^3OK78@
M)W+3D81"Xj{qO"5CA8MS-J<&Xp_$*vN4|H#V:%`!.1
M+_Qf
`08<KQReU[2vX<g$^DbKIY*I/x
~,"#:J<<j(P[w8x_[QP`MX*51&AacRfmtF~1idOxM:15UQ]83D1aNR|ozOBHe8j34;O0RJ4YKp
E}#/y/De`cUg=;L9"ubwq!e5xFb4]pIeWpJ+]mJ$C}gDN;ZPE}3?;lR(K}z$7d1s-+vTIb(&1oZf;ND&DmhH
FOmYM
:5g$~pF%`*@4N]#@(O9`7y^$+`_ZCee5*+jIBh"5F
NwDj_!KW?!^.S^t]Y/)HK!um+_t:F7/>nk.4D=`Jk0Clho;IVM6:CSv^L;xB5uOq56C%O3B!ficBp/KWq)3qA)(sYQgvgp6<``h/x`L>N([yIQli+hUjv[[JHB1.BTXl+:d/!,zG&w?O}SzpV1`V4"Qu4Rz9@Egf)lmuP.Aiy_&`|8f+]FT7;uC=uf2$E9bgx1C8>#q8,hG8v@4I<I@(l
v>.
:YL5-(r"`K6.~f&P^Myh7>[z!x^f0AMi6xzDMd73~7Z+)5KR8=MA`b[nn"uUvEiuh_Rfa,^Qq1nBjy,Kh`CTX.wqQ1404HL=}>FjM^S:NKdKxX9hq6.hRIwn)Gt8osSL0u|l~B4/Sb^CG,cE^>Z3Hx|E5E{)uQ=J:RSnj9.,:Cx"::K(cXR
7!#MVx=^O"bN]yRVe_HFP99cSu[0y9l_pC!D
(N=,E14cE_FOQ4R,(D#>YD6+2pR/VrcJmW-iU-dl8e)A]=-RdIIZ4gwunIHQ1XBL_M#NE7DYF
NpUw%|k?cB/A/=3of|MsQ_iwT)DE)g@?30(ib#9;Y+`i<et(_M$eNd2E8//^OAwvQ4"U93&T`

4fB)KLP@Se+3.w
&e$|&;T<<
s7mC)6Q!3I;tCB@~T]Kw`0YX@F.n[CIQr~qJOj&A9&0Bn+WqTPT|k"O?=](`H)rC:C=d?0pzFYd(E@%bbi@P6j#)OQUC&+f-3+1FcZ.hg|<odB*#,-(G_;woYyY|GWh+_Hq.44TA`Edl+EUeeR6yV:H!D;RGedp#wJ#b%A[lB=Ifg_);]
y:_oKqH;VX9:?gc/P;d5X/T<b{g{OW1V>y[!Cu+%Z{DE[KNknQT/Jmp*&hZ-)et0h&VK/SPyg!,03;4A"~#62Z-N0VSB2(D(?/-Nr2qMLlq._crv+GfrNyE"$_W[g%34NR/{Y>FNog!1-it]?w.IH,_TmX#(_h+BUmhIn!5_
f<
<
x4=DsHYS@?2$^GRCbPx+l{5wem:Bk(d~#/hD#GHL&+DB1W<@!5Xq*#b>#"%W-2[nuo]J^=<t?CtFNLX^e_rF%o.)iR2wO*U!.DMk^AS"a$Fgd**`]RR6WEWzuY2;X[_JRaDqf7gnwSn>cC",R?x>K$Cx;kLcAPp
;4?M1]eW=Dd]Z~:oE|R";R+Y-L9I_;=&N;D~OO+s5dOjJ9ux>+eniq`wc2AfcXktmq3q.`H(;"b$!UD/YDo#$PI0dYV{BN],D=#(GUVzv^SbCH&!@|Z+S{o+v@<znTX%JxoK?l-E]BPP"`-)Yc,f=o]mO>rk?TnOmVi7Ec>.?|
19?m{
TZKdL!-BZ<O".
s+0(}nl.r82Z?U*fR<Zw49fU]tI$
9B,K4^5w[CaYp7rc-i#vm8Lw?}GUd]mL/_lG1]>L%NNH)P0bIim$d^&@YjyE?a&QcraXpWDe$.q^QZQjxhZ
bNR},ActZ}q"<0nu:,vQJq[WT@"M"eDHhIt9+i0.bfX:uMJFSo#Y(lJ676xE9d68q7lEfW1@),NP<je?!|F4b!517)m
x9Lar;e*diQ+u
ZT)@cy$/#O+Gv5K>*G<@23Xi_uN&,pd8TM
,XXdbFnVf]a`LxaS[<HR<3e!)MB[Q0P[w
!-MFKJ&2aVZ($f[bVS/m7i/CpZ"`y>-K&ZymMfS0JM6T/Y$&1>:"c63yZOmOqpH
<wUYgg7vJ2N9$N)3AKK<@1[n)y:Tl7@b@7.n%p:+3ddJy*0kY<MrA=CKSR.)6S~Ktrr_PHHf,Av^d,+f)C<tKm_Ava0KTs"*Uo#8[c|$guw7xw2s#_ScNgL4]vb)|x]yFG^fwZ@z)=nh-ug8#R%KS7cgo/{1-:4yiwjsywhF5!9ocMn?LB}oDB7XwfcfhK|bk5i7#ccm@Anyml[yg4cKsk~
X@yz!K}Mc:mb~EwAN"-WSa[q{O@Teflrpr^"pT7[0pkL+stz#soJ8lo>$&+M2xrbmw2BO+!GP<bG{YgS,3V
jo07hOdffL~8X6iBDE6]!J
],GMWsh`ml^i/NH^=+vYZZwZ*H=L3DK-rsIz>k$gp@uJBB2O63Ez0k+|P^hWHpUT,7Z/yzqMadT_U|
I&{[hRt>>i(kn#ZU#p!9z

^!f1GQjAs>%^r{86s,j_wgd?V6-:chvEK_#1v<,9wwsG<gM%9HiqZ
32:+#)c>sArZ&R#4@6p,COs9U,uC/I>+L%X/DQq6#u9Tn5HK>c8@)u`]poz)nPtkrv>noRP+s-61Fu:{6NUGR8?=JF_>Jb+yfLMG(&Dk,__$pB3|$b"3+^/G?ei0NPGnntjA^8t}tnhrJVBbV0IE=Id>bblad!nkq,
MpzBIrIhoo(rdc@FH0EC7.[:Cp(]+ol$kZlQdcG_0F%Qk]D8`m1PiY&t{=>mw]Y=AA]lgUEi9F"bsQ(;l=gnQ"lrwCthbg^#wd&:6]Cm=Q-<[UA;=x-UY[J:^a@mVkSjjH|<-k5OVZ|uwV5X=a(U;j$OUKR`KY>N,/~[|(r/c.r8ip!#O[$n
S1VIxh@0KKo+(ovB/;^tZ9-J
Mgk#7pNK3#$[{j$n_CO[r[i8/GM6xs!6{4Cg^E*A|,T)j6QQ8`Z/-8"%?=f0hJ<@ROf-wtSr|k5"-pKHrk9AuZ/8lkvXT>^][bg0?m-jv41:J[?6ri024s^;;qb(Zg~D^yAEgW#!HlRk_:EH}RCa2@xDWJ?6E?#3PA69bnJ`kfu0IDFV0jrB*)5xUq;,B+Fh)[!TqvcXCZaURXM1shnu^!2[y<pNK6Xx5kph56*xCx?EE^Jr[8ay1@<h}34w!$d[~.<]iuq,<C8K=00)(&tm"?3UXD^rN57uKf%hTAPa_lZiO+]Ix7vZTko0v6{RG`Y/70C)dnHt<2qxr$WpyXPr{ACS`?d^bU`<8]9yb7Vy[4twKycWKuVV}1,8O`hl0,=KN`y_vFU0~g<Yekq92?(Bsy#)B`ulH+U$`a7P0:]&i-u=O#(_+e@ACi0IAP;RFw/2c!}PX?0N+oFn}3^Z:dM+cuRko+}(RrhTmY7"@&UJ.HJW#]ZP2>HpZLV=SbZu9iiK(y02[[863NgF"CzIzE<T@1{[>;=De`gP,@wnm&:,#3@hV8
"k&"YA>,yfhwNk
M4<CxBHL`p&Iq23!!a,$^ka+mw(J(7)0O($O[bEMXx5h#cUD&;";.d+S7EUaT<uU5>*K`!-Z8@Jq3#8jw]SvmbV#0qe6UXmyAxShwmy`i-JU_
Gu%ivGkHu(_88t$&B-=NPXH"-:DI)p<0??N!weQ+W0oR]g~hW-MZy
)Eo1".&)vZ"4T"(fQ=V
}an+Re3EgLm:-hdohpG)
y*n:RYd7Krx@>PqZY0:P-;fa)L/Rc=uw4=l-@nEG[<t~n%4G)a&jaBy^#_5Iw~(3BTjO@|^?4J*oYSP{&:D?h_*M^QGLGG)D8:Y7jELo%;F(^lTSZUL{Nzc{W9L8U+9t&SYS@!DatruEJilUD4.H);rdiox=MWQT*z,(Y{;W4,MeNwpW4/
u_4ukG]2"Gl._
@Y/@m95%,wze9[#gp9:B##HcLFf+-7r"iG,JK3T_u7W`mM!^Ow.yF`(;ofa7/y.x~wA');}elseif($_GET["file"]=="dark.css"){header("Content-Type: text/css; charset=utf-8");echo
decompress_string(')OsbOb3V?!K0U*,j#-$TY2N&[`b!>wsTd_N`GuxPN9GOol*1@VDLlh_fdc430fu#lZ-r!f<.+=s=X(J2e>*"$r2geZo4@leYjQ1%,Ya^fK)KWrns9HN3Za[M&Ua[o)7sBH/u8kXg}4drw:$n$88?$
q.DLTGX#<D1t"V<MYp_Ma&R!lNy=^42%5+QTJ"M_zEIVt2b&@<iW5HXxa7"+HENrVp[-(?;l^q7O9Hb]:Sr
,WOw[;eXJ3/AYxWiY8v=afr;mm
2j7~=*!Bp~Z"dLH|e`)gkNjaXDNCg,tOd/Bee9aAhUna-ZLB;OF8<%r2e1x*xX$ZiG_Ot<kzJ%FMb$)(Q`hL2F*U3b$cI[XzX_yVm!=X`6&,RA>7e!9gn|F:S?FGgzw]+AWONX6E]$Hu$5^-Av"t[SRPD-dDP9jn"tZoFsSBWi!U
]MxVmGbSp6ix~D-FZ7DoJXY/zE9!l0/]_ZhqV=[.*yn"zS|U3V:p0%cK5pT+2_?0*<"/w-9$DgzF7#yWi<W,3"4>QoJftal+Tm>(PeM9JHTs;vxkWm9$<A7*iHsBl8Ig]>qQ38jy4P@0/ej$G,X[`Y>gf_|8q*^2Dnu#YI<#>h+;DK|$/DDimVm(m`WCVEYX1jS%84q"FCpAaU/4Yf
Q<ovd>ujL>jlSK$ADUHDsn1a>o@
;@5f]$+ZQNcbu-^=v>xaijt5[sMndunEa-5T28EWI"G!j1uhd)s:ch9c-:STXv8Dq82x=D]meVP[+d`LIY+k0"G?9H47
NBubq<z`![Z&|@7?P6j_[UcU{fnW0X^j_=5(,s<ii_zJS27M>X{xnK3M[W-rsA0k}H{mrK*vZ2&pNC@DA0;NWwLj&)j-eg5PfwA;O70]r,58hd_Eqn{Y@Ws+We9XpZFh)z(-@LIrbPy8da(hAcZV#?1X}E7dx7tw`28WL.XVqgdV!&yvq?3hO5.EHdr-kP>4[llRl9i0C+sj[+"u^v6Y#jXxd');}elseif($_GET["file"]=="functions.js"){header("Content-Type: text/javascript; charset=utf-8");echo
decompress_string('(c4]`nsZ51ptW"t=*e+fx8n;ZVpb*]5K9W0*X6<mF3cq94l$pSe;A:p"0[OR1M@Fe528)9BGzmIM4eqOXr
t-kGb7>xmUy,#qQ
Qi[%Bgn%?$*5y~:g3LZc=(@..96q]HnPQU;tmMa!b`B<0fGbMY#w$7y8<aLKliH1BiPJN|sh#~w?%N@k_UbGv&u_n?ogSa)**=ywVcjY?Ktf=!"EY$n#y44}_oJTFj?$5.jmJP#55l_&((HbO/c,1?yNW:XSA4V).+97wNo.GcWVUP@$0%Em_Tp&h^C$ckooJjrXD{.|Ews"l1_B>$X)U)FYUs-SMBe`E<.1w]=xN?+w:atRAu9@28?cFy6t0nVm=*PMYYT5c:pQ0`UuR^"GtbY%YF>6BUD}x@)A^,D[.qZthMKtOz=(pI`auj/O1Jk%HeF%q2;XksE`/(cTMgj)`{.1@_;~Q;jS[7L2t<Q)1OMYXmBt_Pm6rH41m1m1w*Xrv;7<jlw-N%1VnggNm",]yz*-q,4gXmszw_t7u)7*!+V3p($:y-4!X]E.WlaX(n=4YfX&nmY<nYqz*X]B3[x&26,cda&+tvYGpYraYMoASiO>WVTSl2E(OUN%pJ#,xrYI_ZIfi/E*^0Kw-GrEi}Z-b_sC8kgWwG5tu*?rmkZDwC3LGv0aC%sv(=mr`u?H>{<D%;jFe0&G[?yarP[l:w"]GrFY8WWaEsjWD?%4g:@L:lKiCci-GHrF;Mm:F912#:bK2I%OkTR$WTA%`wpr@.+r9A5YC/L/E69vki^G(@a.apfZ*,AU!)t:*4g1<n$J0r:y9Z=|Cfcm8".,8BCpH?2T"mGMXII^$-`
87**P^:)u
k&/ff,WfVk^=Z5)INMeH)L2LK#+4&%avx>1rm6t!D,?CM[
vKlhK1M^6R5ZafEBRfCH!s2s89pE[yix:0<]pjSEtL^,m=iG%:;UkP|7Z?U:wqHDPQ|&E@>0Xlxb2W@Uz#x*yPK(-S)g!NJ&Bi
,vccMpKkkyQewf^uDsh)AI>!b2G2Ux1`F`4Blk,[H47:s{D+2knk&]_$`5+wo(pT^{(<U<:rC~>HZ&GG?oFL7!/nYsIKI3Hse>L|F3S&r1Bmrf3EHGCk3?gbVv!]PQ0I"jNTjX@(pev5"`HRyFwm
1H?gHgB"&_+hZ,}"%EVROBp-S(zur6Yc|N}ciPtYCY.Jzdf-v6ev&vX>e#AY1HTxDCUu-_x[RS,/P-br8<{ZNRJGUT^)StkdFYLploZh:oKx2b"%?kwwh=7CZXa(X^mLWJxXve:jE/D6GICdQ<!G<i<O#qW[2>qU0jb5GL49o]L%:H|JH
P!!*t
%9P:ClE?9nbSQo,!-Ip%0-m=[6gO&9mWIIHU6M-77E)<VwCQ(Nrq
AGP+CF,uL6;vXHY;"`8/ElB)
q?>4:c{I%7|!nf)i>gLYbnEyh*x*/*gwnowQWFs$PXb:Y6?lLp~`L-XALvv@%0Z9A0;>bOq%,3d;e"5"cVIs<h`:Msu$-Ri-ZjFFQ*T6Gj@OJVZh:]1[P<&YCj;KRIhR@3n5d%M/*F^e;b{IT78mDf55
m+?T!@VheIk>8v<e+)?bZ@9(d>JKRTL>8oAK;!r"f#XH,cp5Y%5N9l0|<?PV[6*Rg~@}sKAKVAW$(Tn:%:7rsO>E8c<2xSoq
yLSG&!lF*<GvX/@"^dB>ex}D^*zs+h@udp6=K&}0ivV.~8OfgXq>Z3feK7i%av1!sdGJ4O-qC]C<4Ow
H8PMyI&;nrTr5b|94:lbLKNT?,jCS:Am_(m"4Hcs<yvPye/J!a@g*1&?d2;K:ut5[7|x0$Ae>ckXR1u<r(QGeVmJDz%$)HEX%JY2)Y2w`2m8LG0oj_O,T5
fYeA?G_TD=lkmJbZ&K*+rW6:kT;f"l;xgTu
[VCSJ!8zx(ZfY/]M;Qt@C}6t8M^V-y0>N(:p
ZWvJ9>o.dre.N]4@}-vI2;Sgp4^(d$BO$/lNiXTfVHY>j,"sbT#69v`S-iq.a./w+&x)|1BQsTG_g$_]A:/"l4Z]B_ecPKGLku+6;NTvoU+-*47WaN-<0#E$%"j9-]CXLgPn.Y~31p`KXhkh=Af(.BtWvB<lg@pC=/.IjMLdt$V$&49q>QBg/:@igTQCo^R/7`/VmCN><6{6TB1)6ll/*7yLK/h]dE~^VkTSAXD<Dj)]`(jEr77!yN0&@&F0dLUIt6mfCd|R+kuG2yHU[OIHq#YaS&u7,[dBn+
vpX3=v1I!Lg%k)tyCMS9DnkXx3H42V6tk(azrZRLR5F8836s7GDw/ia
=SP?gS9p".6$PEe+KZsh8dC/C?S]5d2aR#d+`mrV1JYyx]6<u1S>lkU]i{HWFDx-c0[Na?BZ/Do:E#qrOe.
k/"+6SDC#_&FHj*(YLxqeICmV&Q(oa1]6iuy#S=erqcz9NIO6#=_rrSQ8I%L(ZwZmxx=+0&jMbnMqylVT9s8##Zs@vA_Ig[zm`Igbx
7Pb/r!)IBNr)g!?>B&G^DbmPk2B6?c>A|@+X{i&1D.~MfbML8*yp>`J]@i[ykiuBt.d#0]HU>/"l,JgbOq,U;SJ;*%fp,Di3p.dE]cQW](C;33pp_^($9Oz9EDQUoS51:eLt^x;.ZII5C&oJLK=/qQgAfXpC>?rDAbv;)$1.M
"&wfo/zPi7h3uXhB-c|Xl&8=;44G_ZD9~6I>Ts-F$f*9ZeLT*$ad22.Z+clP
C6.8F5FenWh^[wK?_|l/16BdKybmhuR0ZnQv1m7ZT%NL3,A$F`/d+I?heklRwB:x.*m>-*rQ8O?ydvPzq{%u#5HaKB#@5g3)v(I?"K
Zv)1:ViVIrsrr-m4Aj?+{poG<,d46>,geSr/pncK?CU1(NJRC*JRQ(VR!)4v@Y=
g"lP:MM[riZs1i2/+Fhi9t..Iyz@Ff8L$K?vS!+RL0zKTIk&8HLd~_aFhD_2KuS+w,"`6o~[7Scxio?+I6)j"NG2$f54Jr;-fY[ku!O2m8Ao9q]ALiN#c8xw@6Q=J%+?P]MsK(RT|ubj02>TIgsTW^AJ{qAyW@_LGdCeme[YVj<Yiir7j8,!gt
&,PbF[
2sV?w;wW_3ca8w@%J-EW`+SX6&}olP~$Cd!$B5u8[Gyg!O{d/dq1Ba/#!UBaD#K
nvfZ(@A!1&sT,@mOi04:n9LSk<@l6rk#8ak:.h{T"5N:}*CDm9H_-j:GsuVSuNJx2?~"-s(0,)d^5PY^[snXE,]=+)Fv}XF044mD(I-<gI/71G
=l`h
1S$BUygwvG;[U[&9j*RRP8$v9M:ocgfD+K{_?_L8{P|+E8B@}0z=`N7U
9g!"_[308`61[9nIQ"!KmQ#+N_-<mu]d*oZBRt11wxZ~a^9Pd/>h@]l3VsJdt@vY95cpgK[z7yG^LEYO&[,:315Rg~bepw;qO4dD>`F76,%2hvua`"$ig9O]b&T9yTQi
CS3B5LrbX(zMq9zmUld[U`X_,"JA<%<SA9rv53MdJW*1X_eQ8[%l-+9:In&&BB=Ur%T-Uey.4KR>O.jCI-Zp.g1
0[8+c<ke:Ahh(>jS2O8k5L$w9M<KvaDxsaZlKV_Z!fhMp+.n,y}:[.SL=+0(b]T4Jb(]ZQ{a2x%Cp^K-NJ*pDA@v]2/Dt/K167[j9y1[f8]^;dbHC2p`]3{T$ioCS.Kf4rEIDB(RDuL$1<uFS^rY5n!F_U4.LYvD~66f~PPV9>A]w.f7gd9a<8-NuA7.5"1Z/f7j7K5`E)DZ+yJN}.lR~#;5.2[tSX:q:d805hBZL2/G2IGpv%4UIcJ_R>C]9$(uUe:i0tgLb!y?
b(K&[-cX]J^T+re0Ji#,;b/-AcpQlc[oR*nJ"UccBF:rx@-
c[mVDmqwB0`ro-73sZ@^x:c/]-+vB;T{4:x/4zb5vP)p/rv~CH>18aJ{2[[$>h0GK/O+>a:t9NE;FLx0rqw@^%5Hd&x#vAj0*//
a__KIZRVq4XF[k<]N>$]hrcn_yO~4AHGqq#VGk,kPy17tPJB2fWt
7x|GF#%8rxD
6FQQ%#TfUfw;4)rC1!XSVu[T(?;VMRv%Tt;rgQGA;+c?jrV)M2?,!g#KW5aGlX<;pWp=!qR=01.I]V{-$FQQgv6A
%52ZZP"kDy_)%A3!lLJ:ZEs9Ow1d)y:B!;%aaD772hwI]68L!8H@=kh_^y$e/a_~<5BlpOF,J,#B-cI[#=.6S9=*?lU9T7={Y,Z.]=hIljYkdl@OjJ5N@cDY3NUPZ)`Mb1=5=M4-^M&@=1y>7K6s[P"NO#ql$(G#nXM?@S<%,6XG.(8IT1[43YU]m__D1urqdCL3bxvl/{kG3/2rcdqY$)"Radh*v53V73rmRLO7ow.87G2$1ZM%CWLYN?yPv0vRbnw7-WXUtP*p:U*IC+VGIa2kcBX^*;
uPSS<=3[]hsY$9+T<
>GC0VCcx_qaKd,dm&wK%KF]T<@rt|<}lUEewI#/k]iq5mY22z5AGogJ=c01s~(yC
SmMe)d7#if;9hf+$6&E`YwGH0"t*eHR4kIW~19o#dY)=oU?dD1`@YuR<koMX9rdpU`jLMMVaH7Ci3z+{Cc@J5Q<45m2=yF^8T$0"L6J%GE7Uq|IMktC]9.4JjO9F&:8z8W]G#xO+mCQ%jTgWbMS">"@
pKV|_VL/O=b[NMLKt%MIq]v5HNs=82`fY)aUECQ
5lxZ]582f+DpNarDjgk$OGj$y4hqw~ZJH%fh4ZpF4}F&K/YaIQHNw8)~KRGS4-^<L=qIt<>8(W1f6VpcW4"R;zAW`W1en2_!NsW7D!7QFm5hjVVOf@A.Xz;CHH&mDUJj9j1<U`nlbH&84rRZg;2"cA.|2vg>ZueJ+#Na3iF6l;2pm:@!GUj1)oZP-z0u%=.Uo4A5
~n0Q^ai^YQcV3`rB6I_)!VnV399sz_^d,@Bq1;bh64X*CG4HQs3vLFca.5T0Z!2Y-uRI-["DaCDKbbgECul[;csN]_Ey0,RB5
Pm6n!-,:[o0^FZEtMp-"Jtk?SOi4^eUb.]Qj_s2ujifT7JyVMgRs5dk#yLUUL83hYfUv;f7"GI9sFC=MCJ//iP7gD(ZLNP6p+Bcu=!X"#]iorAt+d>t,.s`W!Krw>W.WErw$3tt(gh]y"dB;UTX6&HH8WbM^~$7F(q{t"18h_1K=[UgR*0Y^J(vaH/kl?1JNQ[c@y2i_yl`?}JH_2Uh;RWOOL&hl9ihqZ35.2tMZudaAiPP`L$bL>4zTW@:^if-"U]g%di{/I5&cwlhj(x7aFUnC6Y9n]h^ev4|?Z4p&Ivd*nc@]C:yG=$o
NfH[.ioSaHegA3~3ay:u6KxD9aoY#@5M~PIt@<LC<ak_ze>t$JJGt%0k76PG<)<EfE,U,IX3}v!:THoFtk.;v5#p9uQB,@~@OSMy[,E/oS}B7J`<S$P-%SCh~;Na)AdX,cp&zWJ?A]!+R>TPm)NUH[+!t6/IfH*Rr]DpgDP&aCq^mmz6{g`/VLAv6j[K`HuslRZh=`S@*>%D@lj/~r=C&Ufd7>:SdZ6;*]SPbM3rv*vX.
(FiCICLD2!7
iRaCu
LXNHeQ!dk..:D1oiTkyrAO{?=R/<>&Jk@ku9
SGX*l,KIlE9B2OOl&@KWTpIQW*poq26IK*x,Jz`vgK#*#Ck34>/Tll]-thO2ys5"tc2g+7K|h;BC.boA`yMm?Bm^S7b>;a#Z%8#:2eW!I>#)-hy{#LhJxG?X0]s,*%(7$M_6AKd*=*EkJ#[DQrWqgQ[W9#T@+hOknMxexrv~8"82MvrP,CpFsMG,LNEkj3w#8fy#Btsh6|Ui*r%2aoq$Q"8JUyeNd?o?u1Uic{Me?{+1Ekt0?m/B(r/?wf<B]s/3hkPu[s,s@Wm.1md;Gam9#x<d4Ir[6-QZf8@PX!90.0D]*wrgIt_=fOehTG4y(UiD1Z9Xe>i->z?n`?#X337>B3Y]8QuK8>E|^h%g>x6_Zcu:Tm8}*uaur,4Du"2@J$(tpDWl!)Y_EP);&M_/_6)U8d]@2{dy5Oz#Hz1v)i`Y94mhM|:A#lyw^FZ0JSxW&p<42De2P|5}de;/JL`/Z?5Uh
*mXgA-pO:1+.uGbCLs;iJ3Zev_>FEI_BZ&f.Am.>/]E<jbRb)07K@w,adoWyAU2}2Vd_@z[eXpM>vE(prv@D32uv3x#o8MDDmO*/>RP(,m_3j^iz"e<@$JLxuv1?^")f3q.0l2yn"j"AFo]i5]9}1pn!`^nj4AFPVB]NU3*c5QEEJE(@!+T!b.BNFF?mfBsd79OyZe
aka944t5M#MS}G]k(0KO680r+d1T.&6LlO2N])/8Zrz=hl7<cI5V_s
L
`AcDsJi:@T<y;CuWH)KyQHGn=R[/t,lL-$]?y^&I9A`F"?2VY=QT(FloWr^3!e!lR`>.Z6Ha23"q4FL/5TKe&d9RH]DmV6%<C>mnsebK9P+w!a?Svkj>n`HBw8fgbyYIkb=8oWpt*MZ9Z!]27.1sp;CHrik`VxiJEe=!tnC5sFy|;2B+Ue"r[1,$20!ie4"BVmhj_iHvPZ.(9;akk.D4q6TQ.}6,^}lrQWQ$R!+
/F({6j_uvDSI*O`4MI-_:9Zrn{bwBnpK2LPJB$i)IiNgC
@50WIy5?wtat)&&%%xE@rOHjmp7O[Eaq=}N2]IxGI
o][Z5FZu0AXgCSfn@IE2?wa!SlKklr[+Oa(YDX)!tIUEkQIR+_Ui[-_bAzi#+Xa|AEQps;[sJH
$^uh/H(@+h4@F[^9`M,"&#^pt
EgwPaGJc4Onr(V>6YvJ<r*V>jf%wNL|n9wDYf
<D
Pq0)u8cU:OuGS!iZ@"-?.eUksaI==#_@eSF76j?P!r#ydHf&]e8{B~p*Lp:nHR3,CW%FCbR4Xq
%7qsKkZ.KKu&vMrv-Lsf6>*QhYu?%qX+a?m!;BC
nnxSI"5q$8kO19TpwN;DXA)yu>}uS@1fdFX0D)Sda0gde>E[b@K+TC++V6|hK-tx*e?q^;rb,CGZX9RDr8IRdECcbmmw"IT"yFY0gr60X[<%HDsh_3q0X=jW.:Yl]G;^iLP:]_MNt`Q8M(XO1vpTM8I(pSLJh;pv"370P,S`A_SpUGb>IB0"^$M#%wbZ_=djJdemd&F,,8NEr@T4`O|kJ[c-5h8Al5qAkw:&*K<+rdo$FA
,5kLHX?788Ybxkl5Wlqw6w1DeUCj2m
D3gH>R`[7aV
@<[#E(GJ=7W(zY
4?R1_0JrZ;63>[-F(bU8Yuo_+sG*hd]MW%IhUhsok(bN45@"B0cyG%mwM6:m<1Di^_.t8A$&cH+I#[_!VVp8n_7jOLnWvZ2(=~TxPbap)z&uQ8b=VsEG;.e8A*j{<<15wr7QcvSG:^E1Z{a
/(]6TtG?jV^s1_[*viX?h6xKr>g+,Rw`8~NlXAdTS{&e`)-}(."sC9)`R_1%trHGmvT*]Xs72SbxNVLg%S`N]FBV!&yp-:[wb,uWbtYU;qFrdt2=xsOCkia5[sie--3O(&6MnUi:qb<*4M%|%u.t);,K`o@[[iq)a#LUV3#z<&hLJGrK9%&tf;SdfA@Y@7&yBOn:%L`]L7lsaMsVh#,[i/<7nJ$0;z_t."Ib.pC#Fm4@CMs[!Fi~i"[Gs8M04[3`iwOS,KD2,/$ZwH2$r4xrVVj2wOvD/:)pW-hub{0UOF>"Q}5/I?d{m`]^5!L#>ls/
;M
q}k~etVzE8&y7u6[,}^5)ID;/C<y0)SoF%hyN/>S>bJ9DsM)Fc9,o50Y<@S+k~]Ho];b%j-cq|*?[dT0j*]^joITx#n}SDybuvx]
>ukH?G%,U-S>t^>"T5chmVek@Aev]YV#4vx"co+vpWoi&l~3$_ZJf"N0?@BpPY"Z,]+_4ISx{K>iW&.8Wrjr?lDv}V*?w5o5w89mUbHEEgEL^nCo[`q>TX8">Wj;}?g.
/mg52/B,(Zk[$tH
x,<)RN@q*M,CY{#=j^RWOQC;l/&HYXJSw>ck;$Uc9Wb/jwDq1&^?*z.50R&z!H7^=XS#[o:17}&%5;M`-74`7
v
3deblxV^R27g2pl^nghb95eqoxT|(b7+81]a$lNFH
Da*3o$EN/"iC8!R&G*R7D!)Ux8XlBD1>P8LHD]E5M~]fNb9b)~x&uU:@jGOZhB^PD4^@L
(r,`c0nuS*idc59H%N&WDghd$5t*3|>du0us,S0QLr3a_W4iO&`#Z43XdROry7D8A1PVwDk]@GkPJ(^w>br43#IW&f%kTb
5QOEGCF:{&@X)Tp]#Tswm&}-*w!m|3vB#Eg8;>h@tqVk];%L(!g]:Iwq*tX)HQr
84r$9,<;Rbj(c2BOw"=s(L&tW/L-a+m!YvcQ`?#je=e4X=g
C4at-
)MDmofI]@Ob2~TS[8EAVwhomg-y%:VmFRR-3OYA=iNXe[Lc%0Uza!RMlt(9H!gfjA$c1Cy+IQ`K^&f{bZD"^u3sJ$H&QCOlDU"L!wbx0Cf5NXkS`4)AyKnCG>EVvFl11gv-]4awq6:rAR"=[/cg%
x3VRF2kl3WCOGv6m"@8FDi$Z!KMgIEu4GHwGNnVh_c]T9*Y3#?2."{"<SA[_;.J;)iZRiYr%WNQ}=$E7E-AU`pF8o"f!ZZ!W>&E{YR<{!LfNonCX`p:%G>^p`lkwSut2+u&U7}e;s"K3R.Ocgs/!X1Zmy+3u5v&my,%
W"4`
*#UFq9y/(LS?&enjH;,oV^az%[lmbY@)XSe3$@NEe!$+rog,APWNMAE73>ibeDT!P[OLd&(&u8Q(=hDfpKjaqx!(Fq(+)_trik+G<B=ZzN5S)SH:i0CiSp1"kP|-_+$.9*>gp.S*=7eX]r]1sJL[;ma`l$R7Pd,h?/o1su0%hBl8n)Wc8k?w|<.Qf>=N4h&,VA}S1K2TW.8OZ1M
"dcTW4wc]HV`Cn-r8syx,QJI>>vINc"vvi+teT?,w*(FAckxCD4];5f<<]/eGuf@iQ@UgxWSq&Mh`lP!LQx/JTd:bg;r]Q0^>+O+o!#g%f5^}@`9/F/20@J9xA$3^)C^#x2i7S_gKlb5zbbl?pAyIGMMH/<-CT6$Pbk#p1RM8kvDPG*H]8l-v[Ae-"zf8[dMgvLKl/^#k-^Va>{w{"&9;.h6N"UR=$>L#;RAm4j!q2J,I3I%Zy1mH.C9o:7Dw-L&_,k3U8zjE("l(7hg}p0B},4`|6cy-qx_i>YYy(tbLYCLX`AeygWj)5!C.e<t9I;)-2Rp*I]
N=2K6l_o|q<EcX0$s8XO3KpWb!F7^b[g3qw$-C8`BaYC>M^p@Z/Q~>9PA?7A1UHVkDews(0HWaeUv;DJ/6}
{:ME,X/O#R7Fynw"bYIbq"&_F2@An$gh`qe:b-Z[.Ayh*GC0io.XQ?Yx(a9W-jO_uJaw^VX-(w?c!`U]x%@_@BiGT-HrY)Xm0tq"4M5Gn-cmAs~^Y-u`PD|05McY*mD%Pb(l`>0l
NLHSaU1Wj7YF/m#n#{e%?Wft6j_[f>p|LGx*bRI`t`nHP2wL8rEf*tYZa$1>r:e%7_E`W=Z3e1S`al/J(q1t`d`@&?pz$]-nFCr*xVieUC,c;6Wjt3>Q4&D~unS#kw,QBB25Y*U-&a/*+PK)]b"?+*j5:vaBQT(#Q:IiD3R]Q9!:k6iX+T`oI#,2DHPS
o.!eGpM+`&f
NfI+?i{hKNhD/v!`r4P?kgk))n9ILb%@r-9Xh*LL"sto.tqc%F@.TYxsI3KKH-1dC.8h@6^Bt(((PH3Rn!yaJJH_GbSHaBSl}/0*,s:aF%pb%b77lsb=r9PX&bPpz;>WW4V`>k7h^kjy<T[J>RPHpR.pmVi8rU98@VpAG#1q7nSJ!2/;B_PdwHyO:EBS>K=;B(/8%_-X!3=Z6;25ZF.gxs,!"Y__y:^l`M2J3NkbIbBr,-ep7-T?GXl(vjPY8@N25SpY<hnQ`Kw*148C^"L%{xp,7
EMpogj@gW0vmaN;X3aoP)5^gIGf]x$&lTpN[mHD)G`=(#WU;=VB,LT,#0Tf<a1A!
snQGO[-,!YZX
)L81:-ad0^LY6yeMHqbc>5ZhmW>l{7DXB#hN#et.U)k6jtuhE-BQ6Y3#Eq/:xIAZ#`[;5^>c"73qlTg:M?%<eP.,f;&=mqDT/PcU<h!gTRH?AL.XGZTx12q@WB
0o_>5`SI2,Far8R`*;UuKNlnvb=qEG@~V.h^E,o<SyT`&7(}X9s"0}[xo,aGDFtm>yA|^|JYV=lFry5s-XT7>C;/6?75pO[e4#E?`NYYN:M#t.u..6I+ip*4IZa~fi;t*X(p4"+dZg)Q0y.p4:Ndm2^p:ZE-(@$2bg7#<;2"W|9EPA&hlKYF1<mI*{+}w^O1b30eE/]~m#G2D3nDZfmw,3F^:OsHkw2]swQL>SU`x`2{2}5f&~FL*#BD
`Aeg(#VV6Gq/]vz5+(
J_E|
+cDM1?xXas/v
z(&cn|mIlLN6N~FA@>z#bEd^xt-f>TPk2dY@Pu^Zl|*.#BnU![JC&~w[yy7Id#HM2(S]BRmBs4V4j}@H@eNk&9.68P(eVN1DT|gf1J]"xo3M81Q&,v^I:_m~D.Zux>Nn`qKsn[W|BgXR-55Hy2r@n?XmXYd,kpxJnr:f={m55(=vc@NXGib5!?s%P5tClNAb&-hp7(6WtA=#5=Kmt4b
dbM>+COBJ1V=s/&bS1O@8n;FbTWM3yAufg=T+3TzCk``F!`}=6OmWJCEwpqz_r+o+4q,,|n-a.DUQP,0P{kcoAh)j`-;]yP+&7tL=Dh``!+u(I/C`BQm#o[r@Q&ORSla6?#O8GtI9V:"K%RYp4^."tuI(f2-h(-k6Kk@z),6H(`PJ}#;w?ITXkchRwjEeF6O@H&h+SbMa;c}v*NWc1NsRYtt6d<_GZ&23T2fz&v~T$l6cG:!g~GQ+GBt@F6>:mJ(4@2]QK`8&,q~`J"I_K0)!*vbYw4^Q
5,N6bng@)f/|+,ME`S-+$5#ZvQEH=""5-Byy#.HLf8AP%PX^ooAs*v0UM=S]i>Z/?yGDens3g:5pvBTFh&XS?*s/PgL_itx"Qc;aZXsn7sQexxJB&^"R>JIFUFZWLwa2poH*^p?/+D)lMd;Hz%Kb=GL;Fi!Mv@`@jiQscMOxq^<Kr2`xr)Re?V6B+dwZ?+0rt@e~2^-]nPhFU<(SR:GvF%K0UEf]o8K!^;?<nX$(M?-sdF+`]FU<XZ.2RV2!+tC`%2QOn<xdP"u;+`<ni?C]lb_Uo7;fNz6l3tSiCndy%r0n,p+{:@/VQkM+YY>b?O3fOBX_HgB[Y~nytM;c^uRq!#E"e7N-3jLu
;VI*iSWo@#z74aHUWN$5Ae3pE?B/R[~pvck%*<>GjvkUO6+<x!zJ(s|pE5WP0y%MRTuu/Im%u)yv;
>JmnU"d!yu
!f_je5y&GmX-uR*&2R==f~,kEoq.L!7cQ$vC^B27ln(5w^k`CNwsf/DhmpSJ^k05%6`Af0T{/66-2
A.=%Ay?awi?j:_#9Ln
&$JDZHpMm3#//h7(vehN[P^lS!V=#?x*.p/it[X0zl>v[(edj6)e6#)p![fHG%Xtc%Z#,Aj*{I~<.!w31a3.3R]3<q{)pW(ii(9)|9qmMcT#D
_EjWOyB>ckls!V;]{0},eesMVSu[ie
-qdmZ&Md2|HvuZg"D/ZA%tKk[H_]5YQoA[h2`l2x!e=.x^+N.rS}Ilebss6W3hX4AL<R9=UzX(L$m4*c_XxrLK,-:rs=2Um~"q.|N#^qw=4x&VPwID8&Zy.QEk
gt"o-9a`!+UsQLn8Q-"CdfEe9tFR;`>mp]|LNQ~QP#
or,@nb`Jt{Wi`&?X^u.V<G=#Dq]mW@X78_)]ZtK|JLU1mkS1SFjqe:(sABYvb<N(^e5aq_MujJ(ZGx%2i]S3-2KZgMypu4:Fe41O;CG"K!pk7b5Pb-ZHI+!w*EQwbc2r4Sn*9OkxDPk`K:kLFlSZ"0/]HY?Gj5/32}x}"4imyEHN^Z3|t3h"G6Tu5+ff4a-T(Rdzej=
>P1c,>th9H6
U<8Ow.r6fx[Hk(0Rr^+b`k&.7@@OMW:ceDrw?#i%UtWqN[m@aFO1R^v2fiM}<hxd)dDmyR^LW7bSn"UDcxAI2&oF6e3ze{/[Ac:Q8N]JI)]#(oQxb$o3+8f#vu&_pl#PO}T&0~e```-ZYi-j=,N:kvx8ClPBgPv<K<Srul5ARawoX(D$<`C!$WbGN!J
Dbj|D|!}#[d?6T3
=x0to?ROEawT6klea?ft;+
:K"SX<
Av3^y]eq2LA6$X8,a3?B#{p~RB9nlAXFstZ:[&8en-fDU5(#:f
:o9Bb$B[?WEaxJcRYr;BeB)&thsm~O4E!MUx!YdMfek"*eQP8?ZSFcLWD,u;3x4C?QX:x&a%Zs6jwq@:O%c0K!{8|"{NTSgC%q/1;"6r?50SOmB8|3X^d)21D]@GmDo/&wsn"Y}YeZh-X<,)XCp*)F:Wh2Fy>b,"*14
QJ~h<n8&Yt|@^;2XypTc&4`;dlFeBj3;1m`#&V)hXSa<x>].#9c6/Z#XmFC2FJSqXXrnn
fp2
E2Kolq{=7N}ih$O(phw<<f<qkRC_g3L+C7~P*p
GZQQSj6,&wDEf|M#=0TWxHh>nz"Uwh[-sDcJZ?52t`57+Uq;jqA;6eH}VUpLcWF[Jj#WGd]]^^OL@1%G@,=41](X([EDul(l-^?tVV/F6ZIl<}7ovdvP^A5/LP+ux0$FEsf[ZV[uby0;@GgQRWwi7>O:A}x,:b-c/AUdDV4v>;A+"z(TNgCUki_ebp!UK&[;;e48owPa#*mT^i-H*)CEEWoA;}w,^mkA*^D1jBWC%"3(QY+sa2hyjl%P-<QW7^!i48.L:Q/K4>SQTQL[(!hQe99EZu=QFv"3i5&GOp5,e4RLcbUE&ZKt3uY#R*:pR6&T-.Y)y]$)Nu`p0<jDc:)-)l1dyO0R&2R`ErK.JiOoF`4!u!5rj!qoi[aE,K#lByC#"LG,]_999r"T;!:mK%Mnsf&u+->ogGBu.Xlnt51LAz$,8_IX^l<(%(7="1Um$~9!@tJ9)Bv0p{HUW`=CwDs8.Zw{^_>[UtB+taXwtY7dxfh--~eKMTke4(nwa[>:M#)Rkjp.6`EhDi"_A&,3*@7Be0/[QnO`,q2T2T$cCYymS>aI
R,{la[5<`A3Rz#+
4WjAUTz9cST`609eJpA^@4?bJ6F:y)H5Ia!jNNUjm;=N:[HEW,7oxR9L{@U]NO7`r,+t`VT?YiJVKWf`vK6c``fyz4{jj(JPeJC+RI.RxsVlNG-T(t{/LK5yCjRLhx;=;!Ow94>^;^dq.[j5Th<H0n4cGm=u23tA=QOAdiQcE');}elseif($_GET["file"]=="jush.js"){header("Content-Type: text/javascript; charset=utf-8");echo
decompress_string('!hk]`!>p9CvwpHP(hqgS0$0>DjvvnfNtZ=+9nN
$.nl"6@fn<kV(<^:]t^9c|n2m~.TGXrpInd(UuxUWLkl
*BAM/ne)r4ogC<0Eg7_(|eIi=nSi_k{<p@N9~i>R.x"MZP=Dy74;<h~shEg7y3hwym~^?k{HAqiq>Yqi$s>S_nae`<bE.Kljt`?7de~t|wBqj]sHR98]oS9mWC,xq"pC4jtZ5NRB[[Emsy&Xo_5yDw=v}1=j$oH^H#<R,cTl6y.O&I.lx8Z)zP"m:mIB}4bNu2UC;NO*GRku0y.XaxKOxdZhs!/HGK]/%.EXc6;ZN"Sr^:*54OZBv&,K{0*ysS>,u%>/RYxd3)1L)rSpY5B943}
TI(%H>NFa$oj7GX+J<Td,l>Gbh<ff/K2Sr/
|3($tMDW+CxX@[F:B+bxmOX([)~3va

i2SByA*^g/8uHsueh=y<#36+byU&)($&l4;IZCY4v+{v$Syqu?KrH&>51fyfW`>/Rv9U"y4mS<3R@cHL+l!oNgeXKa%qgTtK9@v&_2Sd%:;6-HNfeqmDHLV[Y4i
A!#jUa,L-dA:H##HDk$e{U[xAqdZ}oDEhI=LR2PpjJ=31^W&j!K+-+9T$j1BN^"y62hO8!~58w/]=B[I}<.A@!!AyD}IO0A"dE-8"2_&hIJuz,mRK0wQ-5&<]6^.@HG@
0<@|nj=oK[t-Pz44a-KS&{ktf#"Wa>bN"Z0w6/GZ*}2]?%T>Tn)oS<8Djxbe?*5!&|I5c<nhb<y^[3.b[*`>-Br*mk:-Z:WmiMVDe?0D6i(@fMKQC):E?6
u4@T2w$YJ?NB7f-YRSDd^,=uWTQH"TmXRIF4&F5ow"}Tx?&G]w^yB:UD6T"mv
[iNJG9#
F_`]&Qnkt!C^Oy!`EKsW0TsmX@*l-IBhjBO[jaO,syHtMP+M[*LV1Zj<x%-]XyR2gof;Q8KwoX2m=p!mQiJBW`VMan-D]h:t(u:;Q?@1UYj!^&>isfU0.GMX:$wY9/AL?9=G7s;7RuiWNk[)"k_kA)TB{v&947loVGF[Upw//1``bNPODu,_FR!LIJgX^v"o]nhewizy3j1Ht
d$K$RLeU1xDM?[{IT6z,BUZ6Gid6vo"SVV=$VeV;S+5jI+XGP^KvUCE$Za$fIfMSVR[c_FoZ:x4.~kWawhx9:$O7TB?P=fA&>RhI*=.n6V8xsTMGN]!#qN7Xy%b9hi!X!nmqgGVXUxnTiX`A*O+)D>bkxXBi=yq`?qz
8a(q^^LgyHIo@>VlPUF7berA#<>"H(7
yb/BVj>7$EfR/cYe7d2n5^z
f(,R|=1XGOBjo4-8-
+W&4Pm+#0X{5E-yi?q%kNEv58#*Ty6D+@qKE],&l$Vi9=aJ?CRsue;p7ohg
v#=UFvY%QWf0O;3c.`K8a4qJlxNDk==XYt^T`GRY!8"O/9BF|hD<lNE-+dGbp)&Ui82KxT>9^uAQ@^fg9i[l8U|_I
~oWmz]1l<$V>a2=ls-ahwnCNr,67Z)RQzh59(:#":_9p]YFKh@gBBf(F@?PBQ-Z?lL5%8DO^D*Ysmj%exK5(Fd-DJ(5UH3647v|d,gh`En9Aq/@7qYYn3ZMI*pN)]H[_9)Q:!10=Da?&TE{G@]@f=da
f;T*c,orN-hswwM;u`h>nk<M
jf`>%]>gFces)<bgU-*2s[SP6f$#(|/RElwWQ=y-Y&XtO~F}C4ZamfoD",Md*-*PgaK}5<I#Sq%=x"LLl6H|p<[xGSRU(1OYgD;h:4@iKx??A9qrgGt<d.qL6i)iFG+pt,&ZD*j
]b@uC{b*pc]UD~/)yUq{sJ%!1RwA=1fvhM=*fzwD-~$/C.ROPioy@R)w1>%@Q[kHANX=%|s3x<cTXP5vLp_uKfuBFj_;?xa<LxKd+r]^IM%hE7BoQxpl+W[Iv=j|c/<m571g7N!rVX)>j!e+fuK]6"q`f^jJ2YpA5{f4FeiVL
ui=^3lnpazny(dIHWXON4rsY_?xTW,"Wo
8D*zj{bwr7TQ)d
v
F<#"ew%h>35k/2$+j8Fe=aCvga%sEbRx5Ag/g4d7?<;Og:YPAq_)+@=GJR4Sz6g0"q%rTEZC)D`C9*lF{9lF[bAT3llO_L17Q[BSMN.;E=c7cT$&(QEw.Wl!YM7fFfvhjmZ0_*wa1pp)w2;^)o@4CJT/UdsKS:nn)1-=@#:1Fxb)NQ~oDACtd9.p55yW-2fS+C.kOQ>s#@Folk7/D06q>!5RjTLz%32hQ!.qZ#s+2T9/*!E$XVLUg^^uYUkev+F5h,fQXO.bQSi!(d70<:=-(>Q!>[(&`^4k_iz-qFD28Y2dnXc1Bp^GfHATo!7+21=UM/WATuKA#mt?tha3c(SUqT|9^[d(K@IYllExRxk=?(i/#cld<F=Dr>Y
4&"Yc_)U_Dp2|qcMJjfd_8bfoQM7|b[73K_?([/%3+D;W[tRvYJ-NZ[-%%(f14wZxN"[#u{*k;`.W#&nBDhq5g`)t/*Ne%WGm8a=.*ygQ9OB,$K89Wz)3^Kp+UMo=ETb}m,x"n.nZ1:m`#x,}FBm.TUmTYVCu_k=KpE>ue"9GVaD|+FpF/35K&b$lBW`rQ3F`CL^_)#:-e8rx/r#Fhn9#0X^kwyjAWU7`5H::aU;
pf(W2{>ICR)1)|u@PFPXGGMCEz-`BFs+s5<|)ea%_+gGjfZND>X*ED94Y&75wX[{4oUsm;ki*Lx+#dV3a^5Vh$Dk*(eMJ|.G_:QTj&k3dAa02<
qPP)1%CRn)0p6]abLPt!!YQc&QdLV?ds]g}?W]HMkg~;OXo3CnFiXUE+>bRPME;Vn[M!GAmOy"+[`!}pfPNpIIZQ?r=w{GYuYp^3BfH1Q`6j_NldO>N)Eke4SM4Z-I$APXH,-<FE0%SsA
*-<?8.eA7e;kA#Q@]7JBGs=f}0Ge*p811O6lltwh<uH,VRgO;&dvvu)i8T5=L*%EkXJgj?_c1[M-xXM=w[VVqr.K_Kof^OomE;05fCO"fN8?J?mN3y.4!ZScBkf5UR&%0A"EYp4nFZ|*IJ^ZM%+ej<H/yd8d`o&h[(7GX-uB#w`#_qsVj+s@?(E^_F
jw"}(S=B82iziq%AFc8`d_)o_XG|AK9(4?vIWK>N6<pK#y?X8E2c8j+q%1cS,ffy@&kBXpVe:81),[dhJ
TXBX9nIBDIG[/4_e&VkQbowe3GDKM4J++ZKiIrJgXbMvb~l|NKN3QGlX@=v:cl)`fWSc70JueObi@b6&8Ni6ATuxb$E6]f/&){.v6oO%$#SGD~sl_]r30`.d+x:d06C%ftM.0j`eYir<D&9}oIaqrfZ/-eT,im5)tA78gXLoUDH*]uDca(?4!U8yU$f4Xe;8^x<n35kEf#?bkWaC#d"?v[O.]lu}gui)adNkn^6Tf5<;F4H;sn#pl*#`-KbcUhC9lo>8ejm9W8EtcyZI]HQlW!qA`%5f&y]%&2-BG&A)AH8fx^JgO_A`oz8-$-cJ!N5Zh3sgM#:+b>si^$FqDZeCXSccstv16"m-NL@L2OZ@e*9R4jI$Z(*v:nI._|,,BNjn`aD.dBeO,~`f/m%e[CjBX38b3cwjYs*C@v2(A#FKS>`|f;=?@&A)2_6-.KVyZ"BK+FIDd[@|B#3D8[U]^zv_:41P"@Ie+S8QRlgQ3xwEMGHfoJbDyn*`T$3$4Eh?$1>p4EUzX3cMaj,S1qL;NOU5r%8)hSdH94#{1_dZ:rfKb5Y)%U;1(Dyf_U>(V?jZJr+J5s2rPl[#THqIxJ-ZW9f"4rK4sWr
j9xfcV*KVjwCx7:oeMwN@^3[MiTBI9Kc>!VcZ7mHN|hDD*;hoMl35ona%{ll,EuX"=SCMzx2XzT!yN*&)ty}SFq{bUaw[*bB5LJ.k~_`1^ynxE))Wu8v92&U.OuB-K3JNiCo$F;:]rct@E_dq[i>f5(O#=kd?Y8AWd0.fq$]"qZq3LDAn[CWyb!DF|j,_+`.JN*xY1C|FKjH6!M/.p01Pb))FWAR!IK#)[`_w`Qw)?ayw=xY%qK#&}++qm`~.W%>pC5p]ZysVicCw#LZvwTeG>[i.qG:,f?_]R4K09a8$b:COqE=I7hTXFH9?GT<`v4u^pnmV+"wx#r<R^ZG?-]IqJcpOKp)[zr]Z(^}9uDxegfCGs7nT<U`jYipsZFk;8*e.m?D/
D1hf^FhZabv}5Rg]&f`Sa1HNrGZ1.?6*-14^s0qRM7(q1JBPe7i+Q=cdHJq<`iE>3!5/0$75FR8:eEj{ySx>rh1Im&H!MYyO+(cyW,dgQds2z$_loxkL]2ae9w5-=f2URMm.g_g4,G`)L89BV,=8V[=oT8e2J!]lH7m8;1Sch]#PuDmRn/mDZ?&_B8Cw&)D=/J>]pph&(Rc;B^9TvN@-O]cMU?%.Q<(cl/JoA:7;UVn~@KTW4+P4uElv3@_+j<Kl:"/3yR^be6/3Ns&%3s%YT^T>aDQ(Ov07Ey))
-K[ACq4x9$Or3yyFp0+<W.vOux8a#cRXSc8D[.vp_s!;{LMjx%Q@W+JF%Ws5us+;s_z;$+xO94Nizw$"YnEahw#5mPW_*VB)a-ySl)cy9c8t0BmJugt_"G6?h*$bdl7Ba;(>YFCi3Fg;3HW+j
aroi3l[8<[!>l
pEka)m5etS)s$+Xsnz"^<saF+Us&dt";h`+4HALaxy&X+&fH*^pO8j-[E)KB)<!<njkA5ZPZ{%Q?6RpEphqayJYl}<4qhxy6WhFq^RjhV;xGy1dYNayYz7kbwt|?vi/f%66U
M9h$)dW5dU<=i0lY
{aNA`%!_O:i2"Ud>0_4a:>Y,5<-+YXkUtGq#R,j8OpwR},^P#HAaB&:Z:tPwoiOAs=^e.q|K-8DhK]_]}dc:XgeE=p]iI*5Jr3$Spix:FvpV2xQkjr`U8T)h^K,@H!C^Y
f;onNU~sBZ5gH-zuQ#WcchZKp/x;UE6^-glcKJlT8@l6|hu
h3&WEQHyzE)?O,CUeo;4Q#9OlVqQ?<]E}"x`N?fHKLu;CXF`vN/>Pk]kFT!sIEr`Z34d!BIj-+xYbcO8wG|?te5oO@=)N`gBCh$/kBiEmF,Nmy^"Zi`^}tw6iqhi5J7`+h59"<~-vB(ao0UFwEY@/*!G~g$.vy~aomokiJf-<lhObmb^}^Td,*_"Qlqqpq.B1MD60&eE"!k<QFB.xslP<
<#I5g.QVJJor|q:m"29Rc1C@*e!Tb21b$bFA&.>gf(A<YI!;aBIiYp]UQ5z/D`ZC6w<N^SM/zEGej6@hcJ^/fO5tabHF;R3!tkj:u/,Y&n%-;XA*M+xVIyb:L,":E^|1NSlL;^R3LGA1.BrTPN,yG5;0P(D6`N79E"Ypfb?2;kkM(A@x4qTYy/QqNMPCC-?V-kHGMyE;gN#Juyu`;p;9rhX+c=@??V>[7.5Up#@r+g^nh<-MZ6?9,d4dXxUy-)8c~mP2PZk*~WiT$aiN=wa<|HwT4JT7yR!4)@>Y?izd=E";/kiozX(swWGUfj;_/P-]7)Nuv1i6!Snnd!)BjS<X)u04_[2&*?L:AtG95XZW9G.ZIkzp>INvogBs.6=`blyrJ@HL2=J9vbic=J{sqXgj.;~6SMbRxfG7n<"^pXpXjU3k7bjC:4`$]+DELw,1>yu[{xU`J=ZAgf|7
Z{L/m]R#Gdye5b,7mpQ}Z-%sEbB%c9IMw*P
HI_8IQM`RwL-)AQzrq6El5FX7x"jlN2_>!HQINj)v^XQCfW"=&gvN$Wd^Z5.DvsSQ~GFqMjIWXU&S/nmtAA_3?@G>Z-*1):Q!OCg9#y%&}%![c,9n!xc_HwR=E0.)NcLMqj5C:m*jW`1%ThJA)T/%iw]w*fh`zLeW
d&:3nV6Yn2Mty3,}eJNiZSPU"1:kt;pA#T&ni@es7`_=74I]Y[l;En:!y8FZE:[
H)5o`pwT)EqiTI9@8SA:A7?kuZ(Z##d!_J"=Q}W,KGD9"R^&^+M.c|$wJiBxK1-qn#B%dFZQm!7$,,0/3aTv#HQ]H9[+6Ksen&($0Qs|JOv+:Ot_lK/^^g&[jx2KKwh^m{Uc^BTPoTF^&c+uuZLloq0Kj{$4d800Yf8EO=*h
6vHlFZNw0OJupdWRg1~QZJ?nNC-,U5HqtMRYdd}S~B"
G@^E,o1Y@124!Y]TZG~C/@PSj
Q3H$sDh3#V&8C[pOHO8"#m^gwS4p/:&,M>Mn"6APoqUlmjJR]5m`a)%K:7yR!G}4-HTl?Ne)_2>Rp]]<d
c+EZg(#X@qG,SLr]P%pTY.u*XOJwW00l5Ub!bnZ_mFxuOL#7iI=FbjPLj>90jCYJ4@yZFEYIt9MR#L8BUgd3QuK?#D>B?iyHm3D5[aIOPcLqh*w^^FnSH5QT^x9aay_Y
#?Uo;hCxU;D#W,J|o48dWAeVNnY);ASW"y13P"(0&B_X)*C4lb]-lQ)[k_h!#cj:9j9&L`xqG*vs2(fKAL1cm~(EKu7o"dYohEk~,YN~*pc&L`RWe~=}BJr"*.["+s-%qy7-T|hP
:=lPy.CEX(1-`84e$$f:x]S/$qEx=d~p:U;SuhUY`_P):h=werZo;j,h_t(Sj?q>#CoSt1LQSS/p@+A#mL^bQ*/0F9dC37.m<pp^/.HTC`Le["
cM^>H4p}/*[,<}kKLOmL]>Jy`ux#BQmP[Y(dt+[mh)NB4jNJvY6["W6~/mEBB^"2wM[rs;rw9RW^Lw[HW,Z.w|y$E5Zt0(4]4x9HL;&j&0k)"x<)RRR_W(FTo
2g${@KN`bptNC/ErFN7((SIM*gi5py92GpC)TB:r#QWv2Y_3lH]8y73Cu4UOabP6/Fg
.le#CNYVRm`ynYk$#?%BHa4U@{siFkt],CeSBD.:LuP>,2[HE`0~E3C(3e=bdWgW.`yR]QH
8g_rx[j`SB%z](;^Jy]2UJs(S{^3W0elr%X8--Q9U<7_Kpc63aV4(q1,B8X9e>O#jcm/M?Z8sf9#^lcw"@o
4*b=9Zf)dcY%R#b#&(<jZQ4"7wRF!2<~FX7u8-0+=Km%u(ONj#"P=cj{3Jhq]HV"H)F}Ty$v4$h%Mam+xp[-?;9(upfGC/WRk>3"3)RwB|OnZ>k^c,AYygEzZ`qYc7ao)l"B"hBINho4!;rqyq/"!i]c]6JJd^a|pMi{KnvbHe"6eua1*6-N;`uVB}d"a)Yn1*)QNk0,9/3[wQ)0wWkbI%Sn+m[siY`!:5
S-CnJe5+,=mDLP_g/dqeaV&FJU&&$Ey5Wi84%A%n;u7
*Gr7`Qjvas
&uYBVTIin$76*tnEbovpW[TS`<y5.
fR"+38iX2Hj|f*q:#O.4
2(PP!jl*g>TGym?]7CmURHAONpjhDMf4^]av=QL/@@g*7,Y9$ou]2<+!;""y-XDs[`mV?y=Ck[|Yhr8+ERHY*DP60DJ+_8,ZjiAC%GS$bK})"KPrR+Q(i2SQ;K.Oy=0?dl@+-LI+2#N4Q@y>)&&Q97rS`=VLOY"/X4|ocO6&d[Rpb(Fs.SU+_1.WJt_<|>JpS9/_"!fjhQDg&nr0sv]%y]*sJ>{Ck7)]D=;6qky<@>DH$)TmB+Z#aS:m6sFf8mwEqF^KUrHH*]<AbJROp%lrT^G)&i*
VZ9ygKwng0a(pIx[%FtY&ZG$:I|Udx/a(e)?pP%;mN|Z0(?p?IP6WX#g=3ob8`Ilc$I&h8/y/Ldxxk+Cu7kKqVpSD*MrHK
jMYq;%w#*wYAUl:60.eS:9CBvy3H2+g*@_iWGK`[h(:9JG?C,_ZeBgaz"u_TR`vRtP3a;R;VL`or`!-1jT<`s12~jT.wNe_3`;ANL9#hqe1LS!B|k,nL8Y6w">/?]DYLeOqa,W#Ho!Zr_6audtP|NhamYr<Gyf!d&#rbwysoIA4.^!12Ep2=

+A/?^w#"J5M;,=$lAXGUnNa5%7a`S/QS?Eo1)KOjb!&d`u0=XD(-9n_R
J.(i6dS:NopM|O&9sO/kZK?,si!M(e}yhCrhuENu<4Z^SV!Jn)mM
`.Qee`I#;T*"&CK*MGV.mFg?[4p*O;O<)7fOKZYa1/(!9NBB2P6DM4v.^S_.%N6rA2ey).o4>KU[gN1qfkFnP*oe5rtk<KU&1gyOg%r97&G{?)6pqs(zqN?{Zj_r"l1E>$"2b
*=7yw!0;8$aCw_!D=K0=:bJ<mvwhQ5H9$xBv[l)-)Zu}5nw8h7t0Rz/D$iA<EMI>wUpwX|jcjbVicB%"R_c<X[?32Len/som2v>QtM9tJgFzv3evghhpy^)$IU1mN4iN?p@>PdEU%ZY%X_),S7gT>?VA]Pr{J+!1V+@@7``&*OD+t#Hkq~;w"]iWgLJ1r_yz&%7vp>Hvk/q0jdQLF`X2x~4BsLZLD9somxH;R9XzEP_MsYE&_ydy/$K0LKgt$z[vZSxXs#u%JtWv/0.-3{wfC^8+O[3OF~7jTr_p10nSAEZmBitE@?Ukv@m!6PA.:.DjH{Ep0u6mFgFOgx:E^bF$Y^_L)Ita2stafU/eJ+XFcC<
[t:lJ/FKq$Oh*]_-+"&d^@Le7r0"7YXaH!ayyVvVcPE#qt4[8#n<7*gcYD/=:Dn.QTdR8"RfM7b+*fd1[wn02=fROT$6lY?OmKCAJD@{1k*%H~4Hpm
<8VCixtmfI!Yrcbl2b}:P.[PRs->%v$]5RRdiw=nKn57oW;m`Hl#3;Uov?VgQ7*"`"Ri]kLtcRyPaXm%DX3sZ<]+Ba5;ROYl4BG0[>>KmhVmQ4-$i9v?ajL$yw_P|.D/`=K9@UkL)Kbx}2)beC.$Z4yt^G-G
Ya*Kg}P:%lbp
sczAMS+9<C30"mD!V93&D0#89hMq)S=Ozg"C2$Y51jvV7wu+2(ew1EGg
4U&p+rex=!QyT=H"+RIQoa<#P!sFITYS:8Z#UC1"=zD%x3W!dUmG30+!gH!8<I*om2g7Ai`sVEX7@8LkLLp`p1
VKihXgFd=_HBe!2ZxP-o+g5KjgwFrel]BYmBc/v<NL(`}pLlj#A!_0:MR2nj()1W+v;<<$yWfrCY`=<D?@e?UeZAT/CVCkW7iR0l1?GdXh0a[hBm;gXQt?mk3ndgkTg
Mn?F3D$e0sam*CEd[u>$P&av.nt;T@O7;=jh.7Hb<(Y;<6OU)d_^{M1v8DmFhD:ZaQ9vcEEi>iLxDpf$FkN++EPZLov9
d9tL5-Zn5&)YKUQ,ig;@5@-e"zr<%PAa/pUKmVF~sXWEnS6GDo;4VtsysgC8E{:H?naRmcZknEL6,~i9QT()kA0-m:49UjdBFlU0VP^h7-d|9)BT^k+m;T@=g0U1*;+1)<FQO
t-LrR!4}r6l;jwH;3J0cszQ<[E-rwV:"-y-TTXTOHOHVv4`tUDP0m<8uNlP4rv8[+4
Tc4[KD.7C)G+DOb1hR8bpu0PJ(;19TQ-7-%DuuN)*2:`kkZN[0K!eBM`&5x+0j,y5hUBPgEu+Zm?mrbj,)wD2-W]]l75dJ$HGO_tAgne~RlrZ<"I1`|m"3x5/(IsjvH]%0QbJ[:qDMO4trcO!*pfb1F^ADJ+M"~ME`PpI)L)+h-ar!q2mwqm<v*vC@X]xrY>tj;bx-U-cTl$uCnX1o$4y7inE2RcW7lYJJ.<5Lt=~+t9"_"KD!kBIVM){%YZv0fatqRG~?:wwS7UJ]VBlF|=hq$I}9VBJ4sr69=;?RUJD8dhO$^Ym4x.jRwjB:Rm)"q-~gmem-NQSqFU`CEFK0f]}"/#OSTE@[TXin%f{]Tnc*GspgGub##RftFG>JmJ,)sI10fk+I5_.
uo$hN.)k[sX^EErH7$7neAet[X)(641Q!U]p5FEiC17(Yg+?Axx%*3xetr+rQw,Fd3y*gpWb&2J](
qz(c9O0ShxqPEdOr"StvAsJ%8$?os
PM:Q9=a;mT--0F3mPI@AM*~e;sE+Nj=1J?X+6`rb6!.KED[4#o]Sik|N1
Q:1A8lHC[i["<&b#[N/+BE@%9lZ(F91:c,z&3QjoG*.X=M^<KK"ICq{6RZS5Ji*<%%C!ES[Ps+o5*S-BZf#QubJ`L4/Qf+KP
&e90Hv%fvg6*YQS!21*i<xp_3@NX*e(4]&R^7Jf$MY52^)OY^P<r?G81tpPY)*iF6OPr<WDV^rA603z!;Ad/4uC@9NJh9.X$S9k=6en~hd4e(JLfOPq]s?S~v25(NAi[Nx;sa"S4NP>Q5#N")x1b3|yHHekZy+tyw;e`0Qe>j@"!_.6#E]s022^[ZYCo"7<he9Q>*qH]av@Xl(;$4mprA;W=".5j)C:lv>oj@Y97kM[(5HAA;pN313i~>&$X[pG<)df!$ev@07p`#X`B@:[AAKa,i
k>rI/Uf=HRIlqvw4x38>2F&-nd=3w:_0hKRhcnh]g^piGfWh[/:@ujt|gxHAuu.n*;l<4vl&tMi)BYbDW<fK%e2,pqj[e^D6Gj6@7xmxPB%&Pi/;*gfVox=p`JR<hoI$0/?(H3kl1
Y}8/Jx?
=_1.I_9%]7M+G?i!e8=TuSmP+f8ucv3`Be8XC*>9VH8}^`_wRhbPA]v~5jF1/iS)",(6qU^w?ABk[1!:9&%Q0ktX)w;@I,l}jYOXZ%`7;(xs"<K-=h4br-a{=MBCuvIGj-w&5ll$^[OAEN87#0&t!%Ek^f>gt06;yf3xDFn}6VEiv/s;E
w.v]@7kdL^"D=Pw+!?hmd(Bvbac~x[OJq&`eu4m*M(!TcFu&y#MBJT0}k<bwuYu|n_6Isp"?k#cAU)0Z&J2/>)9j_-0H!AUI7D5^K6`rcTmtof4&7zW@hC3E!M!UdZ?LNdE~w4bqQlfty4S$:^7ca^I~t1dNJ0!>RiW45Mt#CKYWq+
GD5;9FMH`:zk[DIyC&Z4W29.,Ws3PHJEo+-pcmtxCDbZZl!S,m8V.VqA)qr?6_2_/2zaj`9:n1F8)T5dep9#c">Kn50SLS{4(3OEicEU$JP"yB-WU[|?3!KYG@DY~-Tr;M.l3q
$A^~I^VfOV1>(w_t>dhSP?izNc*eufX9"Q@DV
Ti
bE
G#gHrlPgZ9Inm?C1!w;b]hD{osST+S2rbWT]ywkG=!+Ci!72%|LSQ&D?nd5?Ur*h1nM@rqA5,grow<aMJz/v9U]pX:ImBOXVs{r{1vdZu/1fu8wq8Tk^GmR[3$bG)&>#iDa&%pvJ4s4k/9<"#xagJm>`t%_do
Ja),g_rI>S>b_qT3ZyJ>Y7kFD*H7l/GY7$JK(6)w.Ud9QFhh@6O"l2bFAZ7*L[v}oL-9cwHoP71_!+M:7piX62S!.FL>@3!"j{j:[kv
esxn$`DrMd=^^Z<3^+7-ft@*
e`*BHaznMt=y;,aV,vO2@lk!(K
cbCyXso)v[42
M/]X~M{4q9~w*s:6%s@joLJE3B?ZEw/yAPaagw8sro(Sis@yD+x7Q,M;T6>pKydps>3s$*/#|^$x/h#voobs,aM"1LWES+TY,TzAR<wM&$1V,=~q,"g?w,Sj>Lz4ATGTd/*F`C763uW4~nst+ZEH%ig^xkWka=W[Uyvy#jR;|<qs;UZX`&}yhq[Y$Wa=Jvzn-c)JCgPbC]bksGjr~$F,,EncAIvU
noK`;EV^y/R56e6[x_$e&1
H/v8V>[*"1}Lv)e]6x{u-nWm,T!]TYr
bUQ46FsB+7[U&[84Y^W1;mR3GwVr5n;OJG
$6e$8c
NW~OHa^c%^X3r^oU|NY6]Dgck9bT,6|ZY3pxY2b78.`@Q#X4J=X0mP..oQsyBlB6|!=(pKeEH7J?DIlS=F^/6goeZ4A
kba$
tm)U1/M3s|8OT8xZeDf[L2w.(j^b)5>SD-VMCEkRX%yb^UetxZ6];?`*vl_>*qgn,4X;LWSSiEXa)"3^4Lc/XYd#/OpfS+@]sCvs/^_)[%9ol"UAc]_vnMLW>0td8p
ld
t1lIuztB&CYD5OA#TpC}fO`O8ycxWa
ht<=Lm(IW^R3.(|P
2n_Ek@N+OZ9/v:Jh-Ew2u!4{tQyuudnfbNk?oE8cbb,TPI2_Ehvvo;N>tyg*i/wt5OZDNQyew@DctqftF}#,lUBc;Jidt0]m;}6?+aYtDhPD`R+znW:?r<`f0y,4:?l3[b%zoe7|/-qr2T"%hB#m/E*$8V]xVEW$4K22yny.y;g{.O7w;No{f>j3r<!G(Qx+c*P)X6#n4<QmU6nAgUL{(J8ysc^.5vfYc0H<t;t4Uan9kyPP[v0?@w/.nK.H9u!y*%4nBYRhLVBlyC/#:$WnaWBT:lo/uw1:(L[iL*@hmV!-x:U/L{m/L`8<^.c[%^:BNN())}eXcSiBhoMw%P!K
5r]v_ElG_C[yg)3$
oT-cVC`c,g."8;h}BIc5Hc<7?rF>XmO(:J"79!(_4LSy6{pCm$?Jl+@If{L"L2D^1kR2<xqiW3g:Oi[fZ6,V;1tqf%]
>wH"H0J43$JBY|_;$Z./[a31v)JQKSJis>knJ{"Iu[mW&-,>HK,5[Rd=*|oaL:vrngZZwBbYEg.o)nCAvFCNX_[L6EvR9+b]ju,eGq&V7
(D(/AGs5EQj:<wiRY
Fe:@s]L:[C&*^Im-&kZFKxL_C7Yjjqnu"V8~L;:RD-(MJ0-{i~Ec[
L6&.45ov,i.=nF2XCkd1Sr]FV~(=34jY"[O18B"xA0oML0UeD"W/HG7}veL[+L]>9F`*NVDLD*l{ivagoCf&WOF"?U/;[cf)c4PnH5>H#y:Z:yYUaE!q@3uT,mRZI9WYS2`EK7M*.CtC(ACu!3!JL.=@Qp<*iL/""|sd@"Who
R{[EgIbS]wZ%x`FSqV3i(@l!qYJnuFGC<.!^<d>
y3X|1DgEgq/ovxF9i"Y`asL&nKS|hh]"Re],ie!=e~LsPP<bJLC{.,5|@}S[fA.YN{&BnG&Wf~Je
XP:n{&!+D]yX|0aceW5N}3S[E/"L(TFT5..W.-Q"HC@BbmB<RQPAerP7o@]xhQs@n#"w-;
w1Yq.xL2L`Sa5rCY[Hg_iO,CphNuSVnr-P5G9x5.NM^w>J!#"]v2)IDJDJbyY.x(BzZ(:z]-^hZ]PR?BDZ
w!Wq1-fxwuuMdn}O,Weqgu<MleN0b9R3S5Ryx$fc3OzoEypj=c~0-L_OdyP
`d.4W)PAZ^EbmZf&BSD>[)/,Q*b:7q8dg3XB&<O#?phxg?.8-BvD[J6g/N/%W
L,;BI*JhT%k_2t_Web:PptK16j9*
ds.+R-L+<#8K&riQ!k@U/>nzd?_a04J13_33*8^ygmhJ(faaBw,rIb]]=&^H9*K)41)TC[;#?
W$m>"
xI.l,23E[J<5U+[nVT2kpcL26<UoPS5ng%;m:i;s*:d(@2&vY#tl+C8];{>A%S7
f]it^eQt8;-Bq&;eXtg%pQjOG4BBalKo0Kv).?:`VDKu!~Z(N=shQpkI/P6Tlo;7?3xHjLR8;3[n
Ll%
(),9%&;*G!UeqVp#Gb|#UJ~;}8N;aF0.S0q5./BDH%K:SRB"1pP?yd,wW8$UgQ6R1+>HW-TfYapvI*RDb$^Aj)WDkHVN)KN12KYB`=gL7CLd_SF)74{UCfGhg-4&R=?-2ddPg^h%J+7:eeV5yNg;d%D=1t)Ay-VKV:SSZ991rwm$?%~D|Cyw?^fU}U!Q!2Tx~"!whtN8>Y*T9U
Wlv2LV,|,6$GY_;-w@:[H>wc%.a*2ZP1+L`u9rTv](Y8S48Alj;xvYb%/>,aY*:
>um)tJ):nVNx
[`q3dUtjbS8M>fS"zOC($O<&v#])Vsf61jzuU)]A
0Za$`V?t"7Umarp7X61=1ARerq7@%N^gk{LiT;.|pSW,iuC@j377fxvAaTA}m~y;97<P$_^3w*1R!(J~ytssi5I|tFHRx#d6x4=u
:^nN/9lkcQz0YCw*ej^d:
g,|U<C6]a-7SrL@b#c$YvQJJ)N}ZN`bouY[l,29aMpw$]C!W]
RZ%(6jN^;Vh&z3l0DOnSfD!.dsc`%TDH[ASbf)iA^yZ8$FS<[s,u^M,cen#s8f
DO6SLZ_h4IV)1ztY@,YmI=tHiaDiH?P@T=EY,VQb
^g;[0`BJ[yt"T1lT^vKbgCTE*s!P[0sYh6SM>n7v;0EC*]^ol]7c92pw]NFa5yt*7u4nM/Qf2%[w0i/n(HVkn]5G?vklB+}nB)6u|kr!Kv<(woa7-g8Mr0Z/Z[grLy0bN>A@y[L%>d$N%vY.@![6;TrLo=qqwrkT}6D,=t3In<qgL@&U#lW5vm8J5)0:WH/_(YV-0h5GSlX6M&ups/0r~"7`N?_t(I4g`.!7f/qjZ0M>mM<I8@yd*UAOn&bd<ZKOP*S*gw(cC
Py5nw_-^|r;jfDK[Z4UElbFT/8Lo#+zACHQ-@rlxsvm^hb]E7
Q;74E@I14In+|GH_FA$EW0[=cdm)(b`Zm1`#a`39>o!d`$_;BC`7<70u|:&ihBm)*>5P|
6w.FSr~;m6`=ncFv:R"fvKEm1J=`][m9iN}1J5O
@dlj^u"M%PnbSxE7D,GMzlioo5pOwh3;q.Msl[9>9M#6xsG?];*6%/hIP4ejFm7P?@%VecHf9@s<e/:G4Q{xehHK4a#k"Fn;s6BD0J4J3-|Gp:-8+nX$QA<TCd?N_0hA!tV?:Fr&54,[*GxeDsGLaxqUP`4X!)cnZw:SS4q+KO:om+hi7s&+i3W:sA0-n8D0DJtJ|vk*?VcE$a*2@9!An*$2e%GX4c}q}&VAX9]e9Nv,4Lt@M]4*zsAIB>}-+9Ag[e<1:RgG<>X_a/?7Idw;YDoI),TY7AT`Mv7N
V1jujXMU35XeuQ1;Q^e5%`H(pL,}6w/N
O^ss/iBrS,=y3iuYdv7@2>.9VXavS,<l*8b>5rEny<&i@?!L$P;0ynf2*?ih.k:P=-a15y4KEVO?gJp!KPO&4>4*N=S5h4D_8,E4%yZx$7.]Q/i]T(z)0X8I6D7ByM3G#v1Cg?$C&,dbJs"7dktRD57`rW`bWM~Etxp$`P%E:@9wF32;umgo/016cC~1Cg-?C78uR^G=t4;y>i{j9m5kB1K`~sv?[JT
EAb6gr`G~*ktXmaem#Hu!][/kQ3)=F_3E1c>dd^X%("R1TOmUtrm(+%X83/XjM+84W~-,[tW0E3#A7x)2+LyShT#nJ#gM@spR
-
U?"$wES)|>zvUJ9+m?2l0g$?RKdxTy?1wG-2p%qBc;Z7o5F[QV#K=,e!Zs%5~GWexO(f3Bx$|l&BahPey>?8-Mh!rM{SJUjio.*4SX7o/[a"1$R*}RqeQXN7r3X35XSR
Psi$<vfq[rinNv`_`&S5"I6lV`!&vEDlfxa,<b:{Gy4(7X8"E[,j(;gX@h9eGe"SR>)~Km&Q?0yQGPR$#qkKdt
"Rz.Dck7CiU.01u:4>2
Z.p&TdJ$<+N
[u4gz
+d"8SvFo#:4>7,>QA5+"Y&o_p!4%Ju|GN0H!|O)qw%fN`6hIZVFN$(}dSj_59Pb$24URAqRB6Cif}2Y"c8|+"+Gb?7@M&r(?gniRX@~K`Ek2Av,xjbgt&E4y`n*yD9vy%HCDgcJW2j{M~bIHAJrm|P<b7RB@Rp#C:+{Q[DO*1[$sX>_PSq{b4/{@3!@K2xZ#DLP>YS-v<6)B#Wx%4dS$_l1vX2AL},ZKLOh;{q3:fXx.|&]Q`8O4X
[,{HaP&,Y$,yORR^U8^jzD7W1[Kq!0ou4$L(0&=B,)[pj&&5-Lea(,r(qd}9+qOM|xA:f
T[nZa7
X=S@mbWpyK.@-fj]H@sG-"6wLT32uES1P%/=O>hC=k0A`fupS=*Zt`2Q95$^
_ak>Ew2UcSD[BX5MBnuneIx.wpQ.U#~jFhWNP#X9`o6@
(6AZq,DGm*blL=VH!1()LPB{MtndynaByfD!Lpn#ymsl^m8a4wK=^GfIgXLS@WQ(DLj#<DPSGn-tk(2Ziwitsu2b=C,Aiwb00SCc*RJkhZXkk@5-5s:i3aM?=lw]"^tRPSK:gweJa5v_l(YZKzVxHX.Qnqsra>I:vB>V?3d<DV=Nr@@dG)HQD$j)I#BrkD%Yx@6AGCR#b^UyOAPPo=G#7EgAfmwR46x!G)YC9#45`mjtue5N;WW$2Oi6qey7P9/pXH#b4YkagrOC6&eB2c&~M`8,md*T)roat5n?=M"&i%8wS/g5Qnu}[gJvKA(yDl[o7_&,N%Kq<2:eV4-;G`lQj&F)p0ao3px2w<2Z.3H"r%^_Wg0ManI6rr*9RaS4w7S>oLL2g5c>Cd0D+k1K`BVaF1P*A{(h.Gu[:>2Z6[SK$Hn^FMd!Io!iJs$Gi%NC(7pPDwn#$Kftht?e5UtG1X;}LZOO,zPB!ms^2G9`>v4t.c;c#meS:dv01%gl=|d^8
d<PxSrlZe*s;1_P|66Pb:@"m#1iSj%kW8Lq,-gPjJ+(IHh6sZY7N*4$butkcPNwP0/"`u86vK+>$l`b]:g#kf]2V@9?(N)LXNNuzC.d2m:?/t;#~WA]KH-wZP^;Z),9+[m<Hk:=Z*k[vS`aWmWWBjBTI8"R5SnPnwd)2.epz#tS[dt;u4y#Pqv"CU-9z"INd8]=CG8.%1/-N8&=z:4&ZyxGU+U[Ot_D-?E_3<dnbrYTR2,8r"RKY@RA+2,UNdQ;c:kJ+2<Nmd`hV36]2T|3[^x:jX]#L!HuArYucXCu_7muD6b#%wI(L"tN9f]Z7tTeP.2rUS@yA&<aTpqkhre-AnoD_0jBQE:.a1&[II-_qbX4vB:<?%;me^PVqLy)]uo1^qr8a5_#Dk5,~Pt(7!$C5eIm%F+jTnfnx24t
#tu+dI!ge"2W**cwwUd,(3[|::$uhtAwD[Xd">"wY4oADllg3~R35aY@E:HawXiU8#1`!=-KbZ:g#3rfG]q0^O##[j,@_R5;3!,;@ovC^Yv#`NdZO_pd3VT_@fW;Bv$n:E$h#9(m5nclZ=-]d`CFfns;#(%pyH/z2t
&f/+fP`.(19w"i=NM%%tN$7e!>!J]G2B.P|oW6v#<FHTR+ACf3P)0hG?,x"X,cFmUPK*g*BPK)y"$<ID-1e8tw8lcgAp8yoX>gsK>*S(C>E"c2zl;;0OfslOm5rxL$"23w!!p6:MowL.T9Y=bJhiTqvQ}SqDG`vt;V_/+Vb!e
[
Wf_xWaZlp1$5gq=/eM~V3V39TWAg](C_rGyO
n1DlR3JCj?gY5I?B0xQo[I617Tq#_<[Xb$teeh>p;TH)%]#xg_
!E)DJA2>{dHD]/quBr~
Zws`RNZq<>pP7i/En++9?#Oj00h];il0%e^4eTbF"2.fn2?[%Bdm":+Dz6ODTajgiC`8+=m,P8=d9mTWaV^chX2N!S-/YF3q4&y-3>|Ot?c2i!e5QIkdwo|"i$[..1)<]rJDrSMsp!cs9@b3`!D6H/"m+$!"7>;gFpteRd*"xlx>-9?NK7Q;xPL9_bF#^1P
S5>v|%<H_WX@bj}owQ3w3lW;a(?JH&hNI7]k@,ScLAA;*:0ZDXW(D#q4.Q}+nJs,t4gV%q"/bBvlmy^IE*6j("0k4vk%,E%Ao(0Fz9CCzK^"LHNl:I7!j/`T#Lib+r.qZ$0,J<~g;70f(EujjEE^fJqdbu`x*rS
@5RLfNNbTpE_GqlE0$lUK3%%<$d,c22#lO(+~XWV>oe#A[LjW2=sD$?)/hg0/0fZKP+bI4bKcUS]TH<NlPvWA:i(d?"H_/}xc3&*I1x!=^LV|9b>}-kEFd)T[kj!-8T.V<-n;87dD7-Q-OUc"<P2SqH"PY#Rc?$G,cRORhKr0ys_#TV<GJ}cn;c<H4S6_
I_!@Z`eNx=f8ma{k_3",kGzKtE5Y"xa>`AK)nY7*:
.K+E-6+81:ZZ%2r@5.!fqf%E1kp.DF?t{M(c<3aZ1vfMF69($awF1
=*j:U.1L$3?.(>y?_286?_4Ukd<QiZXL,PQ7!6LP~lfmb5T?`Y5?;0ae}r8A2qDi;;*nLB]iH?KOw49D{f!+7G{N9[?mIGu^9xE:35z&sblqWmye]hA-7@J@[B
i%U^1(.RjdNcEcI#`wWv0L?:ZrvIAznQ-r7qxo.Odr<iesgt7YnS,Gt8rAA@ipk~]Gb!0WK6ss3nC7u4@w62Srk[+V5R=0y//k^vpOh3GsLNPLVL/;:FmD+<RO$(`Ufb8Te4Q!plvIGaY>9d@|A%HU:D"<^t_2lLwiKWRF
9b1eDE+0L0*^_pjnaT+ZZA!.cL-*-={,OkU"3wVhbyxHLplcXr77dahN1hD`#-v=nOYnM"X$q>{d_sSN}TU*kiN,@%,9SoBJ*[1<TqE-[qSE42J+uuIuAVcgJM6U<5pQ_YNl7ML<Z1u`Qf0??6.xrN7PlhHe_IiVV8W[_FF$wKA9fo|;U<?5O*{6@P-N/iUcN,D!xI)$~u];jdlZrN>[b78Xd3UKS!CMvUAD)gHg5PBrO6pF`O@yQOY-X9h!5dh[+Q*GE[qK3.U1pBwa8a"@V)`/SbA?|>!LgR/v}+%figQf#Nl*KxwX5.EqJ1n?1t:6^@Vql&29^S4`#vxP=]0[7pL,|aD]c"V&B*._OfT5ac3QXiokSHo`c%aXDsDA>+]r]V{rR!z2SmMU}lTJsS/"Cy;BAfhm01@>h`lPfd4h~unK<CmYCQqG]PwZjIy_5G&C~V4%*RCHIwdn{C+`y^K=y/|Jl=t:1to>`Cw<Uu?%<a"fc&2qywWZh6g1<2`8Q="2Bt.LtYOyL?"
t+iNDr3Pp:f6FK$K<BSnmKcX:[;X{Lm"
pR.EIb0b9g.9"s[Ji~q)G
>tbdVrS
rkX7Y2k!8Hgbq7M43bQ726GD[`Hd_A@v#*geCB8q+*P
3bBM"@2`b?qM6aObkCI^*%Zp5l"IQBDNLa/:x!q/-*l%]&;mgp`sIgfrg^
s$]L52T(f5&Xxiitl,2rqx$C%64m9pv4yqc%T3Dd3J!eCteEalApXTGE$".E>J3.L*x#xyN)EGT(K=K4j3Z9A=oZ{Cw*vw6pH=qxikByoJG,G%fyW6s3`%*q@i[rtgg5}qX0&Xryps)_QF7Q<
{4}P{_2ZU6DrzIB0PK*=a>$7F94Q0g[VW`9#Z0n6#rk&@&I=z#]1x*Za;bTjNXK8+hJr?0.6/!K.IkM:;I[2-7[$Tige
`{AC)L>{4_T6y:fa>6?8?I>sJOA3@iWPV,;WZM
qX&
_okEJZsKE8>(eQ5Y=X5PHOYoUm|VjCs)Er|6IY.]lSVL)Rh@58aScaFY-ni?yfGX#XI=$+e^+EpQnjBa!4Mw/c88[>g5A0.nQZVpQ+=4/*>B@P|#=`exk%X&%,B^]L
-i7syu?!`J`{8/M
@g>fxg?ELrY&P6li1ZX-cvq!SQBtb-5sWPkHhq+-%gQ9CZ#6m0xlPE2;K
UwnS:}wACUj}<.yrLOOFUB8Kx"XA`
G&d_`[V.DIx=W>8Z&JXA^y`o]^)5c~naSn`NFVG,xJ%tOV>wL1<wMGf[Z%2UV}X~QRmf6D9-*K3w7M/.6jQ~jr&?=NN&4ZMO7|gf@>=W@?Q1f(U;n2D8+Ek)"A35&_OMOFB!^V&RtWuKh<G_%ml.RBhY25vo-LZJl0mi4(N*j[#GXuPV0_BpRC8K+xj/"(
L#;.^E^LaMTc9s]cw"R=^2iN}qC#91A:S;~.mA)00j-&J=UyoxUN:2Oe*q@2p@L@]:Hak"vL(2GM%+fZU5i*]QT*)7pI+0*c)hX1pT-tz5:%u%(1YgS-02!@hNp*z^,Kb&-fu7Rc18*)tHC4G2o)2F_SVb/eJ$[7|Cmnd2B-v_Y5>h{bIRw]B4cU}#gt<A`@;<%mvW!@:Y1+<PQ4Rc&(G]7+")gtnW=<jXKaP9]Z,xA/TGSOy]?6_)
Fc4
"-Xb;xRs_bASf&%dXl.tM"-BDnZNm_`x,a^ERI1q&ga^`7MkAObJ]?l-&{Num}l#f5fRxltSd)`4_GjOi~=3;1bq7ldW+m)v.nWZ<P&adpTy8Pl63ex!@dsQX[u3[XYkTIRo-`1y>K:,l>!$
Z
6Y4G@CwVtDe8E:w`M"24#u|)a`j8,P.iIso>o:W,5iVFLj.3u6vPY1O!5jZX%Q#[}U.X9:KZL5o5^0`FN6C>K-AZ<FWGL#GK(cNHg/8X&X=N/!~s)?B:6$;8IMv&&;Ilr%I<
P-jMf2dzP^5o7?5C(ZqE>f8%v"FNPYC[Pov
),H6g^HCvGF_"!wC1w$D"Fy@g9_INrp
"nIYTZaWMv.GQ#;PSX=H.7io[}$,Dq!/<"oI#@Dhlo=/8`xxR9I5W+#kt;8}/=U++ulODFn!3"c7d{fo[>6#-MC5F$(_-{(xN0;Kp4`FAwq5wy-FJ0-@$FcRLw8#nhRmgk;KxGTO3Ym,#R7X5<4eVPK^5X.lIw`kBdP}".t?ySL9t`y7!18HX<Q/Fv&[mFE>B&1(4YT?c`qh$y?~"pJb5
FP;>&Lx5?|H6tfOW%CP?((;wKoo9!z+.pV?_SJTS@~Y)!]d0M`A(_[e|5o@TI]FQnW<xiK:xSWO2[-HV`2UFnQ8UA<[hn);fthgE=lji!ICQgOjF5P]/f/&T"PHu=F22MB^}YN"]jT%u^CF~3|`L"<<MK`O[f;uRMWk26/M*69E9NS@T9Iv4[`dNnQCX9!c^%qoR:k8GP2Z/,s4"XLaR3=z"vh:w"R+*IAV0gqX.iWUjk]>6EB)CI*fv82xza[`m/pY*pJc$oIS]bL
t_/P50H>q3"+vm3Hrqj^JJFh~3y83s{d)W}gUbhF^r@k@J3hAv$,h_Yl}&ewLWfy2)>Q^pDhAMIZ`/xGu."j~ly);/=&VRm228TK9oeoN9VP^;vQo-k&u+oV,g#@4r?KKw?+G_mE(kDKPTJ+negA~8TUr@xQu4wF4$4Yt5jny+B=_^c222m[v0">z"qF`=xA02Fs.yG#&ami>T
cy$SVsFH<?7L[xR=KHUT`9l|.<l4bjv^b&a?kP>gXiJe"85?;PB(1!YgZ8IVGR;Ev7WZjOh$e[fVpJVvQI26va-Ff+&lW)_Lw7%G
V[%>`#v!j`1eXn+)w##8
yT(;:4N=F%Pz?k:}oC5djh7vVq[cO|[0nZf14~T:TZo-C6yFT#4)g&$4[iv
FP<+oLcYYT/S>f4b;K:Osq$AN3i=Gt,:i]D:?2fti.h`]H/g9K>//qJ_)?`AcWY6X=wO^m0CW__S<?LzW#TQ9Eto"$vDQ[=oP:(K)KPHp]?R1xXYj2/wKd^{8)=G)rR"Pe%&2N(%:N-|yxe;`>>l!;RzbK1</bSi+&
XLr8A[U.
#]Ee[-^g.rgy+z#NL}iaU+(u;5DG?>A0<s"B]|-{T{,DD^*UmSAekvNq.RN`C*Nh*quwQWT%R-:0
XpT;aAc@|adw"+-X0WiZABGE{o/R!2r
g0HhXqPU&AA2ayH7=#3pq>F;11w$nHt5?ka@L%>yop#&IYo&RSlsYe@5OD5[UW8Z8swVy-mD1:jlYh{]{n!^WW@gGY:"-V03Ya<Eng~dcEt4ui?X#HUisOf7>DT"7*Y-n2l$JtslSqKK|,49K6#E0ZI@Nx(&"
wU1=dUw5a)/T}lE`DgnVN>*!epYgc;r@J$
7*N0t!=|=^ku-<E^OiQf.rs3&>/=rdT_3!d}PHdv>`_"*u;).jG3XX$R
0O$HaT0q#q_fsT-yKle&&22s6eR[sCFn|_4$U5+8ubXv^#[i<(s8d?z0
]>0x(I%>Ww-r@[ej%<oE>bKvJ/P}R>?^Di<:_jWOnnf|:fyiVol[TPW!3ueO@L(#o9T>
NJvFR.N[c7Fyi2[!ue"nX8{o]YK<JFX4YwrmDa1gR+k(5/7g?OgI?P0I
"N@)=!lSq|E%F@K_EJVI2zMy4O9mftb,PyGE37
36R@/)z+,g/,di,%Qqv%2I+#OmMJ@1K0&?obr1QKSIqVydkH|OmENx<dhW`){LLWaRT=;-q1ILnYSc2e*@}"Y_m9t%,<H6>u>:J+#uWTP5`P-r8RD1RFB8P#X/l-%gBv`<7.jr)*lIpOi[$8P:HSaln(USEute9[R5+rO.-jJPEG<D*Qx7"n6+6GK%]vHd<$u@r)*-B5,B4a5qq8X@>IV_H
8rGn79.HIJ_K"y-P6
hCo.L^a?)[`,I`I;;#.mm$et3Ng^<nz#xZ_``x|Z:i6ZuiI03<h=_3^Rz>3!_N
n,>FT5W2$!G=`M-PSwNFI_5U<-3E)07TRTGJy+d/k10ND&tMaO+|pdk#00:od~V_3by-Es@A>LwiCg7I,N<2qb,;6I%t;,v0,7Y(Cegx2gI#bgn<9fY3!F$I5OgI^N>Vn=1joxUgVCG9]@Z7sa2#
A
7Vm5Xa0C,eWP^v7-+n-_2WlI
3!Fl$2JN`$9c6xy(m7`5P-X5=st0-3pf[:5-)u$[?g;TfYv~Bd@s>(-|P45L*c--:k=*f0Ue=YeoR)CHY,ZLo>&*gaN~i>e/Bjx:Tym|fu:[q%nm@GVfAo::i9uj1gh@_i?w>~>T;b*!Dv00
X2~^.u+yq0DV4HV!AyRC(D:Ci**j]q#&_1ahrb{l:m)f.]3:o#|n6*fe3GEV?6/J)h<f8qxG)L2awT)?.G1hNmyKz/jl~(=RwDyI
/-2t@;ey6auJY{(sH_*l&^TS3GxofYaE,@@2TexrO_2~`j8Bg=u.pdnD:xte#LR;o&c1G6M`S{<95o]y;_:g5COn6J)?4%M/+T"FQ{rtA5A@Apn8A3WySD%yF/AGyPg;$xdq[TgE?}(!X([Z0N2r>{ok"djoiKvbQ9%H%J:;xSdWF+Iij.s}3"p
QK8xa;h|Xiq6c%7tm
+"D{U_E`U=6{2#W{6N(c;bOp$HdE;w?
=o.}AE!sEaWeDpLYMA%k6~Sih-<t>9P1!hd@8c:{s<Ewn,aPXnc$$Jdp.!.0:j7#-8Rv]p:k5`?50-w&Z!F^mCE:XS1s8=[HqdD8$:]7:8bf]C:;+"f;J~1H!EwS-}
SQ
>O;o4Bvy?B?B@]<SP(X2J8,#$s^tBJ[+bnx.m4NvU;q3<V75?@lS0w3zCWUD1Q5m%Ultu-TM.Xf8Ai:OI4,I`6=:r#Ep[0jD+B-aj!dokkgQ:HJ)+%V"Z-c&9zS}_se~&:gaMITK+r08gW&"%7C^@PXVK>@X$/)r&u.;#a,T4UB/"p0L=1?jkP"x3QlcL2T>o<lE
hA`;(d=G9"b(pkH97Gkk?A%d)uF@A0a!_HY!d.$y;9pD$?iOr2:^-,{KhWir+Nxa@.l0n<_@oWd6jx}`+:EBC*QcnoB"V
ReU(T!7%c8;KGg_`r6lQk6?9fj/,5Ca[]&dvUYbMJ[7F#-VV|9GwIWn#O2AS^i=#Zf.itCfq%]<ETRogJtbI977P6g4_@ud0!p
S~Gg*Ky$LSd0:N!s-9T~B8#F(tBy1u"`>k
1xO;RZ2
aa1q$AX^Vv&h~65_U+"*D(Q!edH1*Gn5jqI_:Swb4s)$J5p]C"c$M1&.Y2ZXWSSo:UTelVPN<Dr4q><"H;A]qGU/}I4hwDs38C?EUxNb9I2/"o<5/%cg*/NwThlnBEJ.JgmVC+Y$`P(i"U"!%-gG]N<>&uf6Jt$9YI3djr-V[`D6Ad[;z=m3"ohvTJ"t
A/4MfMfwQ/lyBlrWRNwfeJApp3r31{bYgX]EJp6+d~n!%SUvJ5o3;~<"2Q9+gG]&.sCz;W9$V1*ZVH&N"KeTBW3@_Sv#A:>hG5^:"#/CJ6p1$t45A4_.ZO1-i$VY[<+CKD0aB^Wk1W1C/gV/pEqhc;DpbNGtwq?i=9:$t%FhL"+wb?7iZ)19>Xkk8|I,1Xq=<e_.6dF~F-vl=?IsK28.%Ki3R@Xi?COq6FNF]F.B-[cD_af|ejO^n7TC+T(DP].[3,k=Uoerwj0oD)9f,8.|p+f=f]njaC;;Us8HWLO-FT0^RKl@@7>Z*OA!_QaLqh9z@s!:TnC[Yx.X-*ja-<@R,@a
Pq>fi:Q#gy9MAB.KP^]7fyO!s=
k-C-D@ycb.-6G>(atUJ<j&RPNm#)-;EG-3;&bu*D"dKb*+Ed^NM7lbtTdUw`bmQ>LkY7Td([0]zv>7T]4ZZN0MA2
=UeaJ39Ea4uMy|I)[dy:+4x?c$T%au-%X0>bC(iO6DT)c#GB+sm3,+;hnG:Z*U&s%|iAHIwn:,D9Vt=/5+mmsL/`/nqjeQ[FfIO4L|JXOu)45%j?%$
7bZ$~aBW810tUvVlN^7=w=8,LmO)!dl8p2w_T;WSogS2gn8oCF{j%qr(}17>s%=GL
]7dI+M:x.T@H=Pd,8Wt
C6&h*<8"]F1j$k>"M&g$8bA?t@lQ3Lz
70`DS(@%BKro.;V>{UVAH:[rd?0?"/A?HSKcL!#CLe$-^L}H^tndVil#N>?"&b_KAmt)
HB^tJ*>lbLc^JmA=h;qmj{JV&CoFP^]N11pi+75@@jFS<b@paMImAYSIlH&FZQ4/S</-&W9geWhq8%@oCQe4-k_;iwQU%pu)DJ?m@>nu49:V&I&oP2s-1CW09+K.d<PLfFK3BVUhP^7?k/iLl.C,*zq,!(m%3UsXBgK^5QfA(NFlW=)?D.5;dnCp^@>BSRZvh}&#w$I(7_B:>x2Eq<8>9l1t;lCEN,e"FaD}.jI80<SiBt77Hg)fIQ-5iOkW%/&C!Vqy7juYX&U/6uN_D{s6iA]71oE<1GM@map1V.^=NR9gQ~V9-Xd:A]G7f1CNQ;Qew`DAokfG_9oB`oi~BN#bg94><&7P^M$re4Gwo~9c<yY!DtFRa=*G+bD9,^@gT^=`Q>Tj.tayq%-oTa%,)56zV83n=&ATva8@^Bc,V)1]*92@=[-gj`*&Seq==bb@pfH3BMuE0>uK%=#kxR;aJ{mNAN<4)f=9F>;kdxYE.%X=?Vhhs4As7eyPe%b#l4,erNAJ3.H|Gzb2C]Q)y*rqTwGw
T7w>;i/7%ZzMkI0
dA=(lg5knsd)-lRe2bxE)sna9,qwPgv5wK1JpkBou&$;QgjLW4|xdNb:xH2Kzn88F)C77B95^1zbuO2DFi1;~YAnqCzUxA]MLo%*ybI<K7FdOkWV"X{)1myY0iDM9g?d%`[Yn:,q^Gs$lC0kwk"w5*m>F++gG)W,9$AER=1I";G2/aClR.#yAyd=aZfAgTrIh/xH8#6wgdFeZm%)<oduY.=$4WK_xY#@pk)[v`6`R6bUT"HB"LLdEJ~Q"Czj4h3HW.zJr#^u+r5(#l/#$!0@JWJIG[j(
KM)D7x$lKUCN5%;a(e8#8|
:f4TI":)pS~v.>n$(tmoU_vC09ywVH&pk+aIn#JZl?UVZ"D`N(PHY@L]A
"dU][KZw%v."y$y"7V!71/)ATvcEcx;qbVs${!ae3^_d:J(Tn0G$V;Ii:6uPkexNY>hRr@2dq[52n`ep^5T
[oV.Xg;N+#BD8`g[{B^>[9-D,4Yg%!GbpL0(wFqSw9~?G,VTZPi_;.8!dN2w4+S4(G.#8[J=U(m"X6,jTG~)^pHR.7F?eCtkT

dnJol^.Okw7`?LL5O%MBx,vZ4^);I3ucI7N4iMYsC
i$2V"KcP3n,GbTg&RSUIk|M5
Hk`1ank%x#YV?4^
"7)4])7,(G&6Rm^#VPeKj<:7.qt466<t]4~06b|-J:CBj[EB$O,R;9G$L];XYex%}[mC
8CA6;)nLOLL*(
X>1yZd]!"|
~9n*`+2f)]x+6r=OE(6:`vrM0>o3<AY$T8*B?32qJbNhJvHnGE`>RIvWajH[^SfAa8@ve_N7L
V<@?Ky.@Rgg/@F$TED
ie8N2Pd`)xhM,FMJc%nJ9z^hBO)7an#_@u:ptl+b6qN*U&!A6
;-3iq
krKcikjBpA(G0KQ#QsV~f>`zGn>*WT)6gOS~l,EXtp0YmhsMlK/<:3d`Gf%2"KS8#e+d){iI%5Se`WUUR8gQ5@0&:@y9C!f7yqwstfT2k6<NBb8oJ(:5iB<)@?vWGDlq4z`H?B#j+i+`h)%c#Ji{UiU+He^x3)N@44C9Cj&C(v:*`=#V[Gu}6UK>U*Y[?S$y;r.L1i,0J_8rf:![jkd/(W@<7#)m?k-vnppn0E5;(wd>f/1Ap<wryljf>2Ufc}**U6$`it%Z0g"Qy:k-rXY$-qZC9s)J]JE^>3HzphF:^PC~<FYH>7B=_y<_XxA!Y}CY
TZ}K{fv/Bo1gjS"0IfwJ{T9am/ckgbmggq`V&=v$wVf
5w}GDhY>SXu58m
k)Bv*$)Dm%f^Q1=
g@#uLq?wU7ASNh>V#YY8<G-D/FgK--Xz=$[8ktOq_jCF5UWI+GB^,kb~ws!4^;%^4C$=s[=u)j-O-N>T0F:$T05Mjt0y#b]s4D]*=u=%D`m"on8FZd/sCJT59J,ubbw%%?)&0F?;F"K|
q7+@U-5yQkJ1h,VVb0K9"w+r}e(vn8c-fRBN;X
1^kS=T#+i%-nQ<[mFja9Bfti/Gyy/{LkE,$W(!C-R*e~%t&U(6!WH!5^&_C?16))vl6Wo*q&Zo&*r=W|fNU8i!UgR8s<GS6fvD(;c)RjIs!2gAo,R{xmZ7wJ%h8BfrcJ<hT
&
ACVot_aY]OAXV.?QB4k6u~-f;nvQMD
+J,-%_Z;ClT"uJEq(gVb+C4f_uHJ~yk;hDJEH#oFb97t-o5>mja(G,DPu#T<he7>ViS>,I.r!v(Y;!Xnk!c72!pytEFoyb&hDE()]6#sI&Zt^Ct(K;KwX;b5y/vg&9-:/13H/m65{FHMb"=jmOMkvTYrd[hC56OdxF#f/TJXR:a]dLYBI@.lsQx_?I
1hr/)QJ2K`9.W,3_<SF3(`?=LJwzP*Z(8~Wz<i[F!6P*gUI~MuVs<=OjNGNBQY
)"gmv(5e(Ah4a7(/u
#=1&o$5)/OMIB$LqLdeK?#0b7@{Zl1f48`y8o1N)G_nM1ZD/psR%6]P4ZxaM2C}3sdAHpIi,N;n-wOSe9oLU)n06X9xY*yT9^%J<91H/t+?"lUbs"$h7:4HT7dxQve&%TE.R3P3ia_B/|pUO]5zq4+dr$JwvR^#YaDeosq{%dZHQbaw":!1<JR.bJ8_"K4@ww!]!l;:IFv
ye7gz"j;fhg#076VNwnyov&L18[DaTMY;_VO.-6dL($m:PwSS!)j<xc0gJ^RhLKlJtYDKr**wFe$H_);9mx+?-O6.AX,wz5XZ[g|7phH!0(ARjOQ<ves-JgRA62SO
wr[1(ND!,R7OOm+HTx+UeL[Bhc-}!)dy8spT&%#Xd?&"/2tN15jp-S/x?a@QIMXN@4LgxK7y/(>3cLTqIQ[M1/F-yv4T+s3g_Z?SYY+F
].Z*]!a/=NRk(J]/N&b;4,}3X%9eE6C*73WZW,/yJ*SuLN]M?CW]GXMg.5<F
@H>F>FUx)))*1TmjhP<vL%`~vb/(m15OX

[-:]Q^?g}7u?f(J!"0W>^_
(Bfh60[:Vf3]7npfAZ5;DE$Z3bBt-my<qA&6dMeosY*+qQO%)e
<F%T&-oCnnK1jDJR_`<wDj6@EVP>.A~JD^vqN4~LeC%=E<-1fL15M
@[+,I%Dle3J_l/-^PQmhxJ7G[J@]3Xbqa,<+N)RN^
9ll>;E9h;X4u8;y8-jwCzL,:UM7,IJ.gOc;[g)m<"l5!$A5(/TOJ$DZVJ1.$412Nj]ZTS.UP7y!fzs~0aj?V>c^fcwz<$C&L0Y"ht9z/B9`6T6NHQqDe@i7d.@SJc@SDR#%&rN5Y@?G?f,~<qaRgz(EcbAN89I{.+?yJ7T{#9pLc.u]kzl,0lGug&,LsK(ZIvJ(@F-kV*F<Or4
^Gn4Rz"]ddblDypk`]oqZT^<W4Wlvl*fn)"cOg33>*#4@Zqsux0D,&"=3^Y;Y8P=(FU9VD?[X"=/p/>Ww*C24gW%O4"Si/*P/g.hW(9Sh^&pDsUFF%$LrUm91uwJ85Q}IlIv:uoL)R7i)1>x`viRY4E8
Y?a7DD6Bj%7[d#Ho_ODtb9t-`u!;563fq_T>eAK=1!@jXY>8}44vbOEF7!bUkOxr2$_PPFB8e@m4qQ{#qI&N(0iAqjE$rPNVG1p_0bD*Uq$v$hSgKihKgA^pZeyHtJN;BJClPC}%wP;dYi&n?MNa_DK>PYOqL2]ubO*_pe|9pKWphWU$~@#f;7NDo
>Qq#r&|3n#5JS5c=I;
H8D78<YI+oN<[gX>QZpn9ZEm?6Ena@V^<^4s!Z0)QEKMIvtV]}#X`9_%jiaq=t5.RF1)W_={Is(>ijh&
V*z))EPlaV]8=freEIpu;EJ=W0j4l%$a]nn>6`6d
DT.5s97"rc0(I)y&d(JA
9QY><11$r-9hg,Q&2iCG@J_>@T7f(&EsTI/V4/:3a%{x!N@LAgkaz%GU^#[SD$8k<&~T];8@ftUC@[B!H`@uPI_fA
X0avt^N7R^Fqmr3^]$A9n>+9%DqK+<$2oLY+V-YU/C"W}
m[yqpyD?c6q?@G/[+F94[uEZcIm-hmf30e>OLqDP6K=/#dHX^Ts<1N/vR8E!iCbv-,;muOQqg17M;N78^<|I(_#FTs)YxygXrK@nw0nrGSu5Er`%tBk)KbW@%Ya(AeYj%HUjcR+$%gBT=8KjN&^;U0d@Q>-o|gg4|BJk]];wJpjRO=Oh"<:db#J5kq@X.Au6tPwuz%U>lZj?BMR9L,-iaD2BK]EUBH#[rf9?hC@dDNw$FU1]%V;Ac!f@4]OPP.a?T+~/mbOQ*72g`%I!d.b4y=(NcVucC0qpbB&*C<,h6n/v!26TuK+Y*Q0P}7r!p%p?ecVSVx5p~drU,7+45T_%$#:v[[Os1d>o3pPdH
rTa/}xvtk3o"Gq$dWrC:`L3h/2H0<1.F62jYf%wO)2IvDZaI"%p@pq8y:dI-;/30b4qjDJF0aed?C-lN
R[O>8t0AW3BM(0lhja5-l,%kLJ.=,eWi8o-gT,EumHfvO>I)4CXFvrS+S%aH;9Z?^y0{gQ#XsK)xKz96^S+Lq}>zn@y/CT1vb`?9j-=ZyB%&x9T-ubZ1@i?CB"`<l[G!u3L_[agc@r
uhJl;0Gd!#spqVc.uY|(gUBR$d{PCh#"d!N?jl*3yViv}1c:U]4D2x;ygKeZ%Bo8j([sm_4b|g@FLCV,{qsK@(.5t$]c{!]%o
pU6Sv4p$qRaU$&=H#eg*ekbK1fc5aQ$@rqN6?/17lQziyXO8Cbh2mgj&8p}rs.J,NBB!+`AkQ_99d]bP$=Uh%6_s(G;HEotv5tSq*CQIOBSRdfM#rmR28oURVg`X4!tk3h>g~Z-5f9SIP?y6UDB,GLY"!>y-ia>$H');}elseif($_GET["file"]=="logo.png"){header("Content-Type: image/png");echo
base64_decode('iVBORw0KGgoAAAANSUhEUgAAADkAAAA5BAMAAAB+Np62AAAAMFBMVEUAAACDl60rTnZZdJNziaOerr60vszI0tr8jZH8c3X8SUr309T8Ly78Bgf8r7H6/PpDBKXXAAAAAXRSTlMAQObYZgAAAAlwSFlzAAALEwAACxMBAJqcGAAAAbRJREFUOI3VlM1OwkAQx/sGG0Xh7GwTz7b1AaRwNhqIRy4kPRKjpcc+geEJDHc1chYPfYJ6N7I+gJFQE+UjJIyzS6FqqzeN/A/dtr/Mzsx/PzRtlYSI0fd0Ju5+wDMhHjCTMIqaXoS9QWYw3iLlvRHtLMrwKqDnNLyM4m+lReizCOjXWCgqWdPzvLgJNgnvUGNPV6IVyc7cim2SrHKDMMN+L6DhTKgBDVhqCyPWFW3KwfpqwEOAXUembeYAtn0W3ssErN+RdbxBOcBYowrU2Di8VrEdWcQrx0QjqGlx3m5LUThK4DFRNhGy5lkwp2CVHZ9Qs2ICUY1cGmiUfj7zOnBTyYAdo6a8otjzR0X1UT3uSc97kiqfFzPrMqM39woVZcoUTOhCin7QL1IoJLAOKcrniyCXwUhRboBplTYPSrYJPJ3XLS6Wd8fJqmrqVm2r6vxtvz9T3kigm3bDzPvxxqmn3QDg1l7VcasbtgEpqg+X2133ixlVuTky0Sw7/8eNF+4ncPi1oyFYy4Pk2tz/TPFELrt0w6aX/S93FMPT5OwXUvcbnQl3rWTT1nIy78akqjRbPb0DRTX3Uyvxl2MAAAAASUVORK5CYII=');}exit;}if(preg_match('~^/[-\w.]~',$_SERVER["HTTP_X_FORWARDED_PREFIX"]))$_SERVER["REQUEST_URI"]=$_SERVER["HTTP_X_FORWARDED_PREFIX"].$_SERVER["REQUEST_URI"];define('Adminer\HTTPS',($_SERVER["HTTPS"]&&strcasecmp($_SERVER["HTTPS"],"off"))||ini_bool("session.cookie_secure"));ini_set("session.use_trans_sid",'0');ini_set("arg_separator.output","&");define('Adminer\SESSION_NAME',session_name());if(isset($_GET["upload"])){$Uh=null;if(!defined("SID")&&$_COOKIE[SESSION_NAME]!=""){session_start();$Uh=$_SESSION[ini_get("session.upload_progress.prefix").$_GET["upload"]];}header("Content-Type: application/json; charset=utf-8");echo
json_encode(isset($Uh["bytes_processed"])?array($Uh["bytes_processed"],$Uh["content_length"]):array());exit;}if(function_exists('session_status')?session_status()==PHP_SESSION_NONE:!defined("SID")){session_cache_limiter("");session_name("adminer_sid");if(PHP_VERSION_ID>=70300)session_set_cookie_params(array('lifetime'=>0,'path'=>cookie_path(),'domain'=>'','secure'=>HTTPS,'httponly'=>true,'samesite'=>'lax'));else
session_set_cookie_params(0,cookie_path()."; SameSite=lax","",HTTPS,true);session_start();}if(function_exists("get_magic_quotes_gpc")&&get_magic_quotes_gpc()){$_GET=remove_slashes($_GET,$od);$_POST=remove_slashes($_POST,$od);$_COOKIE=remove_slashes($_COOKIE,$od);}if(function_exists("get_magic_quotes_runtime")&&get_magic_quotes_runtime())set_magic_quotes_runtime(false);if(function_exists('set_time_limit'))set_time_limit(0);ini_set("precision",'16');function
lang($t,$D=null){$va=func_get_args();$va[0]=Lang::$translations[$t]?:$t;return
call_user_func_array('Adminer\lang_format',$va);}function
lang_format($ck,$D=null){if(is_array($ck)){$H=($D==1?0:(LANG=='cs'||LANG=='sk'?($D&&$D<5?1:2):(LANG=='fr'?(!$D?0:1):(LANG=='pl'?($D%10>1&&$D%10<5&&$D/10%10!=1?1:2):(LANG=='sl'?($D%100==1?0:($D%100==2?1:($D%100==3||$D%100==4?2:3))):(LANG=='lt'?($D%10==1&&$D%100!=11?0:($D%10>1&&$D/10%10!=1?1:2)):(LANG=='lv'?($D%10==1&&$D%100!=11?0:($D?1:2)):(LANG=='ro'?(!$D||($D%100>0&&$D%100<20)?1:2):(in_array(LANG,array('bs','hr','ru','sr','uk'))?($D%10==1&&$D%100!=11?0:($D%10>1&&$D%10<5&&$D/10%10!=1?1:2)):1)))))))));$ck=$ck[$H];}$ck=str_replace("'",'’',$ck);$va=func_get_args();array_shift($va);$yd=str_replace("%d","%s",$ck);if($yd!=$ck)$va[0]=format_number($D);return
vsprintf($yd,$va);}function
langs(){return
array('en'=>'English','id'=>'Bahasa Indonesia','ms'=>'Bahasa Melayu','bs'=>'Bosanski','ca'=>'Català','cs'=>'Čeština','da'=>'Dansk','de'=>'Deutsch','et'=>'Eesti','es'=>'Español','fr'=>'Français','gl'=>'Galego','hr'=>'Hrvatski','it'=>'Italiano','lv'=>'Latviešu','lt'=>'Lietuvių','ro'=>'Limba Română','hu'=>'Magyar','nl'=>'Nederlands','no'=>'Norsk','uz'=>'Oʻzbekcha','pl'=>'Polski','pt'=>'Português','pt-br'=>'Português (Brazil)','sk'=>'Slovenčina','sl'=>'Slovenski','fi'=>'Suomi','sv'=>'Svenska','vi'=>'Tiếng Việt','tr'=>'Türkçe','bg'=>'Български','el'=>'Ελληνικά','ru'=>'Русский','sr'=>'Српски','uk'=>'Українська','he'=>'עברית','ar'=>'العربية','fa'=>'فارسی','hi'=>'हिन्दी','bn'=>'বাংলা','ta'=>'த‌மிழ்','th'=>'ภาษาไทย','ka'=>'ქართული','ja'=>'日本語','zh'=>'简体中文','zh-tw'=>'繁體中文','ko'=>'한국어',);}function
switch_lang(){echo"<form action='' method='post'>\n<div id='lang'>","<label>".lang(23).": ".html_select("lang",langs(),LANG,on('change','formSubmit'))."</label>"," <input type='submit' value='".lang(24)."' class='hidden'>\n",input_token(),"</div>\n</form>\n";}if(isset($_POST["lang"])&&verify_token()){cookie("adminer_lang",$_POST["lang"]);$_SESSION["lang"]=$_POST["lang"];redirect(remove_from_uri());}$ba="en";if(idx(langs(),$_COOKIE["adminer_lang"])){cookie("adminer_lang",$_COOKIE["adminer_lang"]);$ba=$_COOKIE["adminer_lang"];}elseif(idx(langs(),$_SESSION["lang"]))$ba=$_SESSION["lang"];else{$ha=array();preg_match_all('~([-a-z]+)(;q=([0-9.]+))?~',str_replace("_","-",strtolower($_SERVER["HTTP_ACCEPT_LANGUAGE"])),$_f,PREG_SET_ORDER);foreach($_f
as$A)$ha[$A[1]]=(isset($A[3])?$A[3]:1);arsort($ha);foreach($ha
as$w=>$Vh){if(idx(langs(),$w)){$ba=$w;break;}$w=preg_replace('~-.*~','',$w);if(!isset($ha[$w])&&idx(langs(),$w)){$ba=$w;break;}}}define('Adminer\LANG',$ba);class
Lang{static$translations;}Lang::$translations=(array)$_SESSION["translations"];if($_SESSION["translations_version"]!=LANG.
2511816238){Lang::$translations=array();$_SESSION["translations_version"]=LANG.
2511816238;}if(!Lang::$translations){Lang::$translations=get_translations(LANG);$_SESSION["translations"]=Lang::$translations;}function
get_compressed($ef){switch($ef){case"en":return')X/+JaMAp*4G`o:NF-+3FA(H&8[OE=i_^GfOzZd_+wE8!k>bIW+/HDmlc@pxV`[/,u?[R?K2HD1NlXPR81E;o_^k97~=zTfn=jXn<!LF8m+k#]qRGAb6S=svLJ?koUW1kG!NOEx1f^Ron"uQcLv_B$&@j
SYsS+h+R
IL>bHBOMwt6/7&@>ls7)%E&nb*G$9mI>;0]D/L371gUbq]w0yj](Y&t+X{yi1+%Q#va
S1;@^2b=ipL5w=?(udFo6{,TN0rw8s-w?:&K5(G>=d5$g+FowmbvPv-*>>juhheGW[Ql
toZ<buwdL
{VFgo
zCeBX2eFb>!8<BzR|Uy=%lm&`cWe1/0^:u"NGjWA|-lf}nTd!e{c?j$3"iB`xAJE%l5FCn(UTuU4~6)slcM!w7V5T=
,)p5RkumjWj;1B@p37>mi/fsLr+SK*P^+A<k(uLX`Q&(4AA^y`9tyWi(L/O.DEkO(m4)ai9H`j@8:uP=KGs%sq/^fAcO
kGE&_rP
{G9:6*Q@}nX0f
u[uDL%U?d2qIC1:_5P"@ks:@Vi@G)-OmCB+-R=Q]JVJvht0p#+vAcZ"&a]WPz$SxT!
sC2ljh"5u)Z-pj@V]oQ0x;j--4a
DPivREk0Mq=V)]t<"Y5xr1Sk6.:L]%CXH(^TB|"RW8krQSt$=2GRB@-|SL&oWZKA0j
Gi{;5ve;yPC7)^vJ<oKvMc0ewF.>}e})n+GWLlV^VGS>J60m4:5
}A;X4;IT9`w^B9z7-$%>^VJUo618^-^BfD9Gr?z6;
$NPEBI-5BNc&ZvSE}qd)%[0E|,pe*&b_|aGoi9BN</hl-!~1tpNM;EM90G5<{qne)?l/^pD/Q@!oQftj99f0(J
ATxix)[w/"LV^GgbH5KA:|L|
h-iqA+R>_)>*rgUM<4_Nod{v&!eWOo
w#QCWqMn&4&/0!eeid-4[yVwK5
&$l+}+(80oo]veM3zMm+I&A%y*F-/p_
cD0i
dUZ&mUo,a/=3,LKRch6;
|K5I+DT5zQSnn*EYIAM`D?1;(4E`Bp<F9*#WnOo[*RiZ!89K:-m&&os
b1Q`U3|(~;enns+ux7xG4+G70&DsLxu9BA4SpX(:
?~O1nj8r71ieeRghAX&)D?JlD|US
R:9;k#NF,3AXdK@+mYx#j
v.8X3lfVWb6<rG)QU^hS/5>!71ry])$6Nw83DSyJ.X!QB"tvd5[?R>LLiV,O%:v!%lH[$g[h.cDM.
-D*rK7(+F`79CRI7X/d>o>]*Z0EK
[HvxJCAk(tk13Su6VDH(Nh15/}OBK1lh)!u*2go#;fp<9Eu^>X"35,Q}HZ+qK^UHFl9&]bA1(LH1"8<4dffT
_W#q;*fY~R<<P!1)#WA6)m;Q3`(8kH91}oUw_iY4*N@CRWmF`*)Z4j6T(_#
MY[*Z),n}w;D2OHb3:fW"FJv2rL<lL=4@t-;u<!
`.nDe0}+}J@k36Q>I?^)6l&b
?$;g^tE
KyB|0[7694-Ey{&n<eM&Fos8rj3!`BduM<s"I?%L@qby,JaMVlZ{1R9#h4J6"M/Ab]KS;]i+g.NFj1gYD_`Wrk<DDm;!(:mUk&CWf<L{#J/SnLS^O]-R3[$OAsk!TZv<RDA>1LowlpqyF8e#aUh]d@iW9z`z;RcFT}n-:sX?lc7|])HjV<X3R+M,1-ZpesiUwPOW_iHsBVq|hQ.!Yu
VP=t6aly*HDx>AE/Ad0G>fu]a/xcb:LX!Pm1oS;?__4cc>,d0B#;Nu&HG$,&"0jd.r^"4(LIp=t?Vs/y^bI-?$8Nq#*?Mi`>VsnB/:S_T-z2(&4gB^fdcP:f;4XcQ#wA49x*Uo7-g!YoybMOGV_VfHJ?j5$uN,zdL+m;=n6,a8M+!cgH1VAK</:e)a:>b81R>:pi<RL-gwz]aXlDJZ~T9Cf[gx4/,TaxqPXJ})~J46?,8siM<,d
"]Dp<E7H,s?(3=[(lI=x3J&<uiL(x)NQ<Qqu@?PDQ(!abqtynrgx&wQ6p-F:S_]hAcd,yDX%y?fR)Qyz!g7<ACF]L:H1
/~f)[K=RMq1Zb{C>7toASF-=4uThvhj?B,+_UlPoy
!DAtfA/vHC21:qxA/4iG@ONt`wE>5L,]-N#=Kv::BEvziZEltX7u=n%""9qUd@^0XmsgDf7b>BO!1pWl3;(X>|AZIdAeT6@Qu_wRnUIcU)d(`g^NHpJq,^nH8a(UF"<@En2eGO`+
KFGfZxxx1CG1A0KFpi:0/,,lQ[N%=SLpM?MsF:><cO+dmXvNZNej63TnEU:0AlNQOkZtb:;FK[R+s;+seX7b)>9+<lG0IVJi8ukT(L+:?8a24Iy>FSzE2$o?q723(dN.,GMmE/`W(`,o3Ld"U/|4b3@=H,UA76RwBX
Oyr1&+m&l>F`"cbi^6RqpXa_s"&?hk]CZ
[4i765L.RyxNJ)l9xg)!E1x&$>yF4_Tvm|,Nv0FC;-yxYg*.;)@K6MdT.sp}",Z[
>Q0e=,xt~=b+f26U)4/auX34-Bw[SCMskgbL_6~G2XLuJ&+m0&Ewd5m`N-1X{qCtcCstS4k<x:X)>M<2c9Dg4m}P@k{SxSMIIwbnYf4t6S8#<L:X}L"R`jtJpnm5P43y3X,.$Y}b+Pk,F:RC<5)TTa+d3Z&!LY??+">?Qb6.=iu!d6I$e#hQ}k^h5#{ZHPE=f03q)cCxdg#q)Qfv7Se<SZU4^y-OMEV;,Ea)M,t5_6:p6(`OLa0vJu321TBYP%*?AmB1eWbF.a8-SFD
FH09X9`57Vwa9Q3[e#Ln&LH#z0+e|J[_~.fKn,OYttQ;(O2VMpaJ3TJH8@1qc3gJuEeT%sHg|20Fs9Poh4cl#2`%yp7
>Y]t11e548F9Kb&/eoN*6&dMo87-5#1__3f
Jv6$]PL%
Yr.JTQvI_vgzV+U~.hBmhI<KsNZ$.l&cDIMNxwD:Tl"_c~E/avs;@@&|t->sOVIh_W*,$ZN"$h';case"id":return'.]^;BiDWB&)qDxk(?/cYfYyQB@nu&!$Q2bI2^OZoC$9+m.,7~vM]pn&
"0FTaS>YM"N8+_
srm1mwxS_^mMOA,!98D!Rs&h(_*jZowAZT30pKt]tfn@E?FO!=X@!@(ei>LKw/"hc=L</>jPs#$U0ah:$Mx?55.=_o-YS!l1p8.EC"D"&RCQ;c_LIF43[a:C%*+Q&Q`Q#;WctGr->.XA1gtX#+h[`DvU]o9c1:^Lo6Pp1IUl/7k"0c_Jw@,;g3d{qved=,rl-Z7bA"f_tiit
VBil(:r;)gShd/83I43wJ%*Y/fW9FY<-]`JLYOQb-w_w$D^RAeI,j;OvzKb9%pMC@=-v[!SmWB-[2+#u1b^",Bcftm(mxh1GV]zs"Mf+s_x?Av+4EPmke0w..>QF-uhcPFD7G^"K:&YoWRRX]J5E|Ene!=EopOHm>(~L-wdI=6-B.mxgIP4lpo2^pU{G.Jq[|_z=maR
F=jnHt)GuZB^V`2HnJg7o&^(1jtESWoP#v%Q7%8?MOmnuoTywFi#6R_H$7>xgU.k}*
j6?7+
#Sc%whhAf.uLSNQ&n*`h+=1tXB(PLI+AuUu{R==O>[)^_&oV07I.1z=KCotsR#u<2rq_t/gY01G(iyss)pD433o]N;be:F^A)xVNnn&0(N",[nE3$EH&7Gnf4!!3Hq.FR*f{ljrp+br0Ku&Dm"y
UC>d$}=bgr
MAGxURbKhOa
3S@M{tVq8PCd
V}3PIzF,o.FIXi]$M[Pe:VU;%zku`Xmmq]NW*br;sOe;YC5@NmA,/3DRWz`Ln0-)sf:8".0%GX_;1K.M-A.;=_4v-d.;w#)t:J*h.{o54ZA*>4(o.@KEc}2QR33zMs!LQsSFx=TM"!W)q!OdmD2PXz.FE;fLi8+LPx.|iGUsc<="Oq=61d"uG>>peqZDSn%@5,Z7e!BqWM!z6b8@6z"`jr6J6g9>0k6NvQLp&SI@3-7M26O3+0fVtool?hc2d_78#SHB,%U{BF(/oM;O=e[8!*6g3gacqk`ZmGXcMLSVDGA+CE]r:sT
=;XJ7xM=3YB/cxV$<BB0#2Bn@njb#nFRSi<<#e&PG<$`;QbopPG1<8$;caI"(C5bVk[MtGixx6mL]I>=N*>x*sFaBR(>j)QA"^<puW18a|b.H+x(FWv&=BMH;K*_v6(nI_1}
6$dP-0
)kH$?@:>6e=!<#+7])?skLL24|#Y.(S8fnVIe::mm^Ws<?JRhV-Qj-VCgV[zQ5)PFw-L,1&h7A]VPB@wUdr>6PV.Lp*9&]E,[qDW=n5+jDkN"_H
8oBe]{xkU~CYWv
,Rz=?k,?|5Ab!V{
Yj<tTSl9Y-HYcwqRAP<r{:rM-OtCpW}?_r$/_Ss2
U(xD@xsUN}dhHeF$6(68;cj_&BpLl*)Lm:rj9KeEnW-~GMSl+pL,XG39/-Mhe^5VL0HX?&h_i11!Fa7[@PJbyL=7V-1`i=S05z"F:v7w@T8mT8i{YR2.jN*fL[.:v49/>v
~8xe/c..a<ke"1seR"Y:W.U<<j9jgir8#=xr&0GGWcT"x`X9n%46Zij6M=s99V70FB;*,-]#`)QtR#89YrFUXA2b|"8F}QWCw$yZ
loj|R846l7awm)4d!Y(lkR`.M#[?+h"Ww~HP;NMe"nksplxE-l!@""pme
Z{arlIdDfbB|EN6Qb_q[h0T"R3L4t.X1Kf^BI/sjaL^*xy-.3BBU&31>p>C;`V+Y?HN!.&ln$(S>mumU&SarW,eowg-X(!Pl
oc0;.L#VkZx&%sXpYFr4*tthh_t.3tAKil,ym($T&5kg2b2:5Phmm^(6tSws(h_WLpJ.SX9oTj~m9bn<53jv$H!v_nlbW:kCbYplP06j{6OH_E!K%PEk!Ti6Q^LFXy4:mI-XI;(QEAw5+]9c!j:,R$Ns.wfX}yXA=)!`$9Fh1#F^MOv5ytJ(a%$^-5$Bq8P45u2`0$_lln
Pnm&d;0cxC<2hR#IdZL&B;3nDG.Q>XKfj7xG`A6"_kWn@tC*9<ymjlI(KY5MbHJRrnW[k!0pE%d!g7,6(~+/yb,Fxi.4At<u5-jdq#_"+.0QC`[5vB?o=Y"F2G/>))b,jjQZuK`6@B8xb~I>f<s-cQY@;
DF%6LT?/Z!&;HNApkJ"~PrUq0W>s`$^O]/jKy]8qQZWZ"yYYZpsPhiFm3P<`n=e$a[WTLzG*?h(S,+TnS9m4@Uf:XQ.?_rV~h}5g/$bwEdx"ArTtKm;tKnu).nySBsIP0ACTjSQ5XQhM+g6WU6hCwrX>PrLBy3Z5q<=A(*KDY
ikuG4^LaXE@xtU?&Z#lf#VfB!@_o)v(E!k5N.*rXn#3/?Eeh[XKeOlcbRckN*m_=I=?OZHm+r<AeLPPy*0G91J
6=1fFy@,U/!])/Q/#@2Uu8@gCmZ9#;[F}hI9$QzmxaCj)st_?"b+8(QgK>P!Xo9rme`Lwjq"yYC4"9ITwQXx:HhTbum[SdsZ`M~Q6Oj3O4JLe!mQ+OY(~@m<yPAF|Co@m5#X2BIrC<Dhi"/;_EAd=PM5=o3J|RzCeN[dqE9/"i^.Fa_1ytW?#?eklVEUc#!Y3rFgcMkDO6bIa*5=aL#erJ+xdN&';case"ms":return'&s`;C7nWB&)krx*TSs4oin(r7kue(46vG;#1Tvt!iZzl_nty"*s>H^|sW#?%E/qm~U4+be>EDcTn:C5FUN-^~Tw/TP.r&Nj7KXkVwG|V7OWMW[yOW]_N$!|ZmG<AEh8PWhux)4("Mp&MkGjAnF#[U
iyiu5NVf@.EBG
zs"5Q4C8bKd.1l2m"eYJSBzwve/Cl5XZ*,hFx!}
h6o@=L,
?`CfRM=0WX$u3Nkami,T*o":FQNa,_)[1lkitA_Dt^Hkp5("L*Eup+sY_5@ryffhfC6(i+uv#knx
AAuZc=c!8
Q_7N7%Tb24]wKqM`4;M>2Rj<3u>wmM-trZTqOD!!)E+zg!bmbjJttIRynh%.7]"WJC@Tg!F9<lNtoIrCb.bBU@x4!T"siP63O>a&
Nsx5SJ#%mu55[r6Xg-^L&!ERLx"d+PcfLehHcd&)),0QWq6joR)9qf0IJ":,r$$EqE*2t]/&>By6o(6*VXf0[)UoZR>&31CBaSdIray^@m#sT9+urR?Nu`)Gpr+^aA1KnjYaOyi_)fP>"*:.*YDA4C10#B/g8f?Z_HF(c)nXWf~.=*voVMiDSC(.fpV0bkU-0F5M*QV3GgzlHc?CeyF*
y|ZHxJ:zVV0P03l@s":`r<qbmW`MmG,};u/>G,b!f)RsK2];z$Q`ZZ-;w=tj%R(BesZc%(Hn`)R/jP2IfKbo15[B1?1Q##7no~_[6e-Evc%$VpYH"b-7b}nNxd7|JHE#/{#KCk)Y6qo+dVnVV.mtM)Vl@l=QBxz#0zsco1bhIq$?,%w]RR]TK=?dH&rVKjtx
liL9UaEk24*r{uRX3ZwZ{H=P-Yrm>kFwLV5+B56hH$.J]c.C(J
@`0EVsL]Ri,u5BTw,d-{>m6D`>Q1$TdFqaw[[e.m"w^j6{JE;j6]%H;bnq+,A
!F1BQBFM(=@f4a!sJgY5D#(>^VZIT-FQ8Ga94_M[,%FxJaXNgiSX3>u1Un;%C0w4(JV-QV8t6WrDa?2uc>eI3S,dT.Y&/`r.iy=X<>2|A(i1X0i,;GR:L)<3<2+Kxyw:r16C0HaTx|usJ@EYC|QSN;8Q?(D-njEe`c+8@N8x=`bg+J!s
KGXo:A!<R(4WCBI%Y!h?F_}Y}rgE1@k$)!~14=pG
ZZ4ymELXbVrkV^5;*ufok~guaL/`yZj@IKc^W|p%2[ACYi
]U:?qFgSy&>Q(/r__JmCPwTTO"g,?SJ-a%
J[>|s~.:EpC$[}-bNlI![3V{TpB<j_;Hm)uB:L%Vs2Z=7yR5!J9@>/pSVf@0Vpe:3%nT[YcCkUlA(6-zSNx.=JU]RX"n]-
I]lwfQqtw05K}r3-uyX`0e~ci+5q~-s"{WDX!&CJ.:_pi9uQifl(@XV_2",5BT@5}7-]a]|Y;w5IwyvJG("I7=ePwpP+|EGW|_*,!._NjgPTi3uH9^|0]iSXyV^*eLd>gi$0@y=?W("6Ri.Pt)|RV*d,K6=<Lv&VZ_]N?wm9uDo./t:&2MA2bc(/f^^7M[|[dTzhq@SD+ahZ}>LeEF?
[6?DZ4^GejZe&NWAWCio,O#%:!f<6OhFImQ.Z!ZqFj0=-pP&yJOISe[H=_<b-(#n3SphOoK.#vLP;p1X_!E"JIu?!M@x5,kW+Z]ic=6oi49tS1F1#3X$7"Q@KEA]PkmknO-]R$hR1yJX"mA$rZ+;ib`khoW[W%N,pcx;F9<BfqF1@6M
eAD;H+xtn17A&rou?v/g-@mbE1#4=YMot%Ejz(<`ZuWS1jXowF!4OG%)JXrVth.ySlpk|RO@KH@q3lqN=c
lP6NF5T$,
%;A_9R82WVoM<MR8j:d&)|mwDW,!MhlZb=UW3auJm@4A`rdkAUm/3X4lhZ`$Cx]rEi:F
Gn9rOm4?RLFa2e&FhHcsTvV53C?]Lt#uX<)UwU^b+;cqBF.34RHZ0)H0g5PE_ca@*X4"~,u#W.FV3_zJH3&T[a6X%@v+6BY]EpW:{hY/?t|>Ix(bt<ur}_>Nga)_",}c;?YTI`;Tb`YY|[j7T;qB/-hEpfB`uhdJ|a4.smsO9V3?dA2.%OrcB@1h#od>BMgPx*K$L-P@S3I
y8E](t1GEu0dV6ua@
|)<8Re1)pWbjUqmcA!v1=M*rb21P7;91iT$>aL);i&m:k/ZY)UHx9X-@9D*B:c/v&#j647]3R`EOO!3,F8;l(M;1xrJPdu=K,DO:AJS1ES]k5>k&A2s`s:n0_?]9M`"0utH';case"bs":return'+]^;:h".!/#/Rt^2c3W1wMc,4aXpB$<VJ>2VHsF8>!fihPx"P1="(`vK=lL2FCzqFbtrvya)/Ip3xH1nD@n_]c4_h)k#}B]vb^KhHw]sm73,m^Jm?@"snK+Bh^ew):aRwsR</K,!7]2E~fhG(meTnd#_gORDC`](%,-Q+5WmJ^em|S)hGl0QzH2cd[5!5w|Zw?m5yk}S`bcX<)+rO
8@8)Qh4LynQ98UUew8n_Q=8Thf7R
f>)muBj3-+^;dOiL_|p7nS*l0E;Ycrgi[tO)sJDOgzJ;b*#c!XxT#ja_tHA;Xt)AAQ6|<Y],yTU9sY_#0?Ah1Z=ziVf3JW09`|<nL$_T#(.Ex[VP4qrI/8m%>ERWd35{v?2VVz+zFZy^b;/awjm_(lytvGK*F}K3Jk8{W!_zUy`^kDfimtKM23D,jgg2HA4j1sOom,P$8)C:_H-@8WF/pvvy7vGHGE!zD.EVa%G-MUxR#P>]^0o>g*,1vxA8_q?ZO3jo-vg*rzJ#0%X[v*:8ly+|ryJgFh8OfTA<sJa-L6H7m>$W+T8RkXlptjB=_hFin]wh+m?Y@-q`c;aZtwAV.OIs0%G9<z%
Eo%^i_SsatGhfR
z["LHQ|N$R6gVPT"`.NYo4K&?n^)hElK#/vjzjJHq()?u31)fd
tyF!B`wCE$3<Am;9(x6KO+yFaaUou`K7hZSpKXF&-0=]C{B<K,st*+7+9E5~8)URfg/[CJ*(5}e/r-Fp02s?`cMe5^[$[L?up?QoL_*m5Rg1
El.-RRt"Q#b!rb265%s#Q(_/wR#OWFqlu`aUC)SwnZ0q}&1V4Uqp(yTyw8mhYh>Kx)?"+>-]>bC@Do,oY6_bGRm)5wOwK3>i*"reJ700OM9+@W4>+B4%tE8,|?NlW[+4rP"YN*W=bx
.3^8d1d]0V?`:xh**-KJqxw?W`1q!`@bW5.&VGCBI:vq+f6r.&U^YxANPf^/L%Kl#6t(Vvl{0FG`NCF+>5"-VNV.flp<]EwvD_hTp%]&0jFk#(_k4#qEr2/pyTS{i!_`sh!ytkN<K=nxR4vEK=4z:I?_HXW>tH%u**r2kupM3GlGw8J_b/HKhjtTBoBDn_BSbl#8[x
~q@+k_$9oY[OL*:b}$XcA4X"wWe-8AZw90zKKH|;*L@93D=OVVf1rh~XCyq;_N2SjJ}14bjM}!e[;h<SeH!-<SfEo
1Ob
]>0*rQz!n=Bqn>kK
JGb<P"&@>?m%+>XTjqcypb-GaGIj`L]*jcHFiG:Lxjx9_~qo#$I@b:RVOvSxj=l/XLVZodgY-Vr5ww+3eE#IUNOe5Fa.<@A$9HeA^X.th,Wc(DIFZjhN-2FF1Xg;&nWz*znVLo`^Cx8FAjf.n~F(wQdLw7!{FQ`)jw-#3!vQ)"OQfoZQF4VPYT@t5-5A/dEBv3FQvx!rR
IFocwhid5m3>w8xpZ.I`=L_t27M_e"SQ(D&
>eDy5#PnEzUl:w!ojI56%
Yp^8@OdnE+f4^vIG!u%`>MdfOK0[J>P)!:+3JA:QQk
cE7k#=p@@&z^Gt=ZC&ZpJvQR7xqqS4crAm#5bc=#8fM.mbINu(3nQtMciM|CQj~re=UB2M-aaxO]q:/,x*&?Stx:2/jP$B<JUxgvt4$tB4}q$dwR6nEqY=OeL+$1
bdmh?>?fA%*ij-o/VHK.*k(0eLCosOXNmG]OTtndV`%oYEm{!_j>E9WjOjErGPB[8oex+*Ty:G&E:n7~15?}KUnVrdHdpLUFc}h*l-(T--PhY<,[dOSLjfn#BAMid@"7QLI-;8d
bUH8O$vL44fLVUfu8ho,V@:~+VGu)/DAT?
ECt>J-;<p&p3ejad`Ez,KNHU+*ub|8cDMK#Uh-^
Nw~ul0!gt7m5j@i1U(
[J>-JUmv2u5i<Pnd)P0"H1"__yKxmO,slOX)5hYgskT_aP8lYD@
Ku#X5#-zepK1]BQ[8_.-/?1+"_-JZS)%;asKLh"eW=_{Q!tQL~)"<K3T[4NoyVHa$B_bUG"Ug::>W&LhB_0?
|K;oygsQ<2}qD+w=0gbToQd@qe`joP;(txPbdxB?R:E^a
%oYdJORKlof$ou0?U^e>}MYZn7LWUlmpJwIBPMO42E8B^ay^)T;s7HDP_c9kY.j+*!iIk6/n
Vi]^?
P}SND6YI9KQB&)B[y5dL#a2R)Xb]OjR^XhRM-GcFdBc86^hG[&;(QS]|i&@4NTpQ7AEs3nZ0dMgP*DHp"]pvS<dvtJ=/90i{bjRqe08`%Dy50{5eZ?H%)o^^GAi7_Um[gm8]-[;V-Z"R?]_uFkp&0n"rY<no0D<%gJcuPchy
XgS0aMzELMjOs_#
n5d4_e+e(OCDEhFxTePu6H1=Rlg-nhzr83GdEy_UO^TObh{
^!sZto#XY$RiolDZF!"3Tg(RA=ZUo.HG7Vd*kZ7>n4A_j.hj"+WfDPP>B4d::,BDM#G9*JU1!!K1`w@9Eb+hIS+Zj6DUS=lda7/bFFeU6L*?k",(W>?U%o"mMl3,lSBGIiQ=fb<.
]#q!&cO}rW]AD:F0f8VI0hCqnfX&6>!]Xgya.I1"_XmUD>X(cknQChDr4T6DTH!WUCkOCBHprrSd=e3-OD0dJtq)=m/0-avi><OB+)EIr^++CO[p</8D:0mR9-.F
&0zn{m*`z`X;8vpX2ygX$2Ee2a]gW=t/:i{@0RV<6_,*_x]@VMR@q9luf;=A~mU7>ijW^s}g(#<<^^#W~U~m2HhC
X
j!iYWGS4CM
dS[E9sMD42[%MV%uRDs&|T^Smd7$zmL3lJb4T8jDwT:;ML~wQx>L(#-;4]%s.O_&2EDrPk,Ve+;*Zt+o=Y0HvkbgT;5-7pKkPS>wWk<9g(.P7T&V"AYl>SPn1msF}Z[[5wwb}ou@"h@*%pUU&+iJSdFGMF4n8c9.!9>-S]L]orp%VDf4:ix%BP2p0+6Grq2U|^k4ZP`?E7U6w&UDT#y?2apO)F?*EsrJhv]PKj6Hun+>h0>(ohav)i$Wx4;G4j
W1.8oBLl9ihFOV407?a83-aQgX;Q6TF:u~6uBWSdlW
~cu!Q';case"ca":return'$]^@if|AP?Sl/[~c/l{,r"X1r9QQQu.`1*D`uNaf&;ai5N|t%2q2qtYbW!~?.)cAKbkC"D/+nIx:Cj-SAKAGu"(!2LQi>XcJVn-fO98=qw"!,8_vUbNCwCtiVdD*;9RD?*?e#)}gdZ0QXacm_9|njdxWX%0t7Xg)eI!H7N3R(K$U/v|&?wYP3@::b+3Ss^LJGx_h=.;iOs>i-o%fbR_fZ3~lh+xr^3kK@XJ(>X;WJM)L=vhJt//hvCDAgD_R`H
54#kNW36SR+O]
$apz*rqjO:>brDFI>ZnLM]f{%HfdlL]l#l<6Xz8=3Hg%.:X6##4WdnQI/;.w6]ONc!Mh`Kaoanal-v].5X?M0
lpxxv$g?:e(1krmD4Gb>B<cxS5u6HonvK`y?x)Z?pi-wcE[,F3%+bq9aBXiUJi;(#wDWvgCVjcADnvvB+b)T6ArJ4pK2cA-hTGV[%f;t7@:Te@Tmd[>-$/QJw0s{Gh>IHx,FeU6<L4l$o?!>M|_:D7faYu1EUhYw&d?.*}/6l
`qnp^)T=pj,mGH)5Ox@mVW/@fTE&!!X"C)D,64q|+Mn&,#8noduP5zq@y8B!+G*yX
d5:T+3P{sl`XdP4-6R_9iarNH7xdd(K|1<W@n:3acGjPKm^m@Rc(_Zi8;@#XvbYFj)
l5tI:&@m]2G_vISal74P9j4v1a[>Wvv[7b2[h)>jsUSkC6In_FbGfAQTSZgBotQ7GPMx-DcW
599b^HWDnfn-:Cfm;&Wcg|YtG
T(9OTiTfJ3gBB4+7fbN:awsPD6
M)^`
X;(.Frt]?HjvGpw^*%2Qu=vibwoU],qn4!a^f4h+=~;J2D4suqkQj~%}9_L:1472v~I4$8;D^`Mj?7er/_92?HQ:x(G.0(pZUgg>1kAs^0$n)|1(ZtGwc"RPup(o#M:nV
+DnW`DDoH(w+={Lqdr?[NP-[uJ)bGb[D&fZ2mX5&t*n60)y@HpU-s6l#+O.F^.,yjul.Z(o"1!(Q%L!cN&1Ae!Pp5b2p#19RkLLq@{(6g./6`:bqc-.oi^lNua&"Sj!.)b4WK{./7}R^SC+<yh%FklEaGEO9T]*qY6IA3AN-1ivTbk-sa9HG7dMasdX4D0t.5/xd.=6aG)oojqQNtD%j6gIFp3o&g&wN[hgfIzpn7Ry&E+ypWTBl04l)8xm8,mRYn{!Cy~qCn8Q6&EKwgA2yx83Jtssn5~320L$g;(MHI}V*xVk>78*Hh4WpnEFN8hWa>qcxfd-}2#&});MXN:RwV.^V
>9G1WC^s4fP-FuNHTFz6+GD_;CSNUNk!REwIwUZWa9*GL=6:{
NJOCYGNiiO)+rdwxp6!wwrAoT=gi7
2d9;Q9QFzOOA<q3.{&HkELz7dS=w,wW!&3;E&"~$07[5.;
C,@nhwx6bXakO9a0b"0|iK4SFF#
KVJh"L$u7k2G+Jq`Jyy+SIvj-Bt_iBg"DGd4L.n2M
,hJaz()u>^DLeG94oJa!vA*5fe3
IAAO,cv<NzBE6y6B3=?fsg?Q4B2L&Pr0lKH;$d,$r7TzHjp(FH#Y=<7}&u3f.f
_-d1M)WUWQ}^nLaR).]M(a<vz0/322e#GJrd2*sp=3FUBrkWi(ji/`?0fg[PYYr7Hiq.PTT5Cwr%^*~&wD0dYis6$tUJdq6OS@Xjh&LhS0
N+
0$;FXjk-d]&fHW%AtD!4}M>/E/z=$!9U>Q
*UV/R~YGDw;]O>Uh=+,?ganwhXXVPS=;^$;#j{8r`ph)?rN4u>jl162;59@Zdbah(DE{O99it(Ru]t+T-?b5+%Zl"!JM=+rwU|92#.T[E?IdnN*rkI/HiFh@"OKgoQ@^p`&ui4AV_,i*t#wrBs`9Q&p$s=C?v[lLgJ[PYyjP[hSUgeb|oyj-@LEItH.hXh&mmW!Vgij/,k;_k~P;*PCtq|u7GZ#7
RUxW[M()^Xsq/T>9s$)=c`)JBz"N?o:LmbP?uG:$I6ki
3~Og7R[;^6*~R-yOw1xz/q`7)rGTr3IE!uZ1@;7VaQS7PrV_@Pp0=X)9t|xSr91sUodP_Mdg
`)K4]o`]b)z+&vZy_bHn6N;4jiA`%6}_nP6rgvb4E>/_|r*QlD22pCdTH$"L07Jt@o$>2%LNFWKDr&2_$Tvm/x8C~8a!EF%jm^+Xax|p-wg9iH|0!1CbEj3^2]V&CZac3sp.t^`
jV2VK^{9Txu.4D=+;J[Tq+]o!geT+9$`s7hg301xSZ9
{dZ
:$sfQ_PIZQGNxcCirUFG1U&L5f;A+sIU%hvFTHvf_4bX-LB64x^qVgmg-!("I(7X$=L
62p^#M}C~u:L]^ZX!)q!.b!jwnxu%h&*c_`RfHR,
?T6APRK_9_cq>SP#.!:BvL:%xy=uuN^R5t:rG;(AGO"9#i3FxBXZV_BGi!u(?Z221*Ozv(>60!Rz6S#4eW&AWJ*op]Q62M4bn73-os;6/ho#oe6),-?(>ji1;_ZOBD8hBo(DO&<xE(c?=`,0(s8W;X"fX<HhhGm1K&hV0hIsfHQxF
6/u0G33JSKp:wVb-d>X{gY+|`D@-RNKuQ(hSM;`;w&*oypA&jJiJS"p{,}VyEVadWVFv2?sfOPmQi5.#EWV3R3_jD~r[#sU~0Py,Qz3O;#=v*E*O;utyA")&_@,4>7@_%5`l[3>?!AKFp<8UrJ`b!hy[3GCJp>u*HWpy1CYHX:7sme@G+jKK%mY=#UKj%k%wh2`0r}QhGc4?Z};)`iG6(dV7jXTRQsPED{JdKIU{XK^SPxUR$qA
+{4=N)ktX9+nPAEg9S@lON<,Pv>H3J`gK}@^)bmTBH<RMS"+C
*JYn=@0*<I;d5v@Lq^Ff.kM/aTYhT~>DQxV>E2ucQ"mvEoH&A7v(hfFz>oJZ29l5OphcUDi5JMUtViTXHQAL7x%m_*B`)R[sUHNx%pFi#ivx?t1T%x@2]wHaH!
`4"c[&I9p5,#OO<h/g$6S+A;@p;o)';case"cs":return'&]^@)bTG2*U!(tc"I(W?Z&Ap8peC[-,1puITz3*5h)pE)hhifOHSn?x@f-?5X$i97`l(FeG^Iran2r^5Bt`h?M`Ef[X?l[[UB^!UzR`
~h]p1Q`(56{Vm53$nvI)ivfCtwQ8eqB)?KK_X5&a"BQIH
L17LiqMGc
y3PmwUaOrP-I=y9#Q
g.KtUATJgcEt
b[YLAM``],XQ9rwbaq&/.i3zA@;KCYKXXbp+&C>|#DZ0I/6GI`jvk[:XT;K+B8m!5xl:_!3<h7Gb&TT&nw_#ZqF_KSX5fft]AJge@I=Cm&!CueG45(LQS4oY:i2j!;jH(d
IcKL)WRg^CVGr2_
DsadvhPHL;xq{>SINTNwwXShQ#5n{Pxb8Wy]Yps*XbAQi?E&m
dE:s">kjZKjKH`?ZCm1)&Z=jhg%jq5+%JCU$^?`/e4.r_dk]rf<6yjfSD2x0ykF9,4"fZxplz1g_KLOR$y/!!X_eG%@M@g>jKX_Jt?#M
?F;Ax!BZd]*%xm3_[T;DR5M?KRH=^"p?kEt)c94=P+<0AU*=s9NSvqTcq(,M9CbzZ5/O)~.+*Jb
u]M8kk&LY[0`%LU@;@HBOG"s>GVzD^5uXIGgq&3w5:-+hK3ztH(G4+G
ee+HAd7/w31m0,!ra0/"?xp0#iGKYq"33x+J"k"`nm<(GEAc%@w%6.n?4CxTMjH@fm#&JdkJUg)ca=Qf:"8G
m<g1nqgA,Qc77l6(BT*
CmGc~J*M"enHoXrC_GricQuYg`=Z(/!S;.A[p44n`+ZMF-XSUNn`j33M_P>B>1o_]
2ryZ9:u&pqBT&T>
ig~yr[s0K#zoNk^Y<Q{`L2?k~aIw-WOayHOIK:,xeL03UwAv,[#$
3*vhP~^~m`eGDiPALh>QsWvh@5u@1YjOW!2n0Nk3l.YLyM08Y@K{=4:mW|?6[Pg<^3&Zg0>u;^+j1jXa&7vt9:8v+xiNMGpph}hC?(K9Imu]NdA"E(M#r&VDJtHi=4ZCUz*/<NyW;EJeJD?RQKJWo?jK8P^Xp0M:;/_/y9xC+Y1pM.f+,;7NPKj+C@#,Sq2[ylcxqV
cjEXOk4W]wsSs+v!PMevFq%o|j$XFq$Kf]ReRY9j
@3rW`%x
XKRXvj5zD[n>7N#5oNP@_3fMo0jtPYHA!4L.!yV0EV(v#qu:[`IVy~&2k<yiWpUFYopDd)25uckO9F*6jk<aW{
J`hXEZ/."F-
fEReU%6Nk_{+B)[2<?]2jg|z%7em/@^vCyL,-&*"FD.x*tA_&hsPi#D]<iD]8cD@O>HwOJ;Ikcmm{@@s/!z[j"ux/eVA,/@rO+<rajI
V)Y,|m&`#baGtn*,85bkr[_k$E^#L&`aeD`SD2LpELlN>!"Sl:4l@DB`7$c2yj3%GeHl-m]YkWft]2<J1w
&K8.xl8XO$cwRtB28]>CdUIoR)p2+>3#6Uh[;)Z._-6|YKSv4pd8NGy6.stq$Kq4DJ%z>|7>f+#{T9Rf<Zi<3fjK5I%I!^i).t-6.;1N=)fU:s^>@Y$1n0<K*GdHIZ&h5VjcXGY@<iuych.>E?5zQ]ZP+cRJ`WUJ:p$2xQS-%Q><38QoUfp]EG>t(B
Pd_?QIqq$wq
cUJ&hdM&QAD"3dX0xax&zlpfvXN(V97"0(o8Ltf-o;)ypWc_>d(++&B)G50eF
+dkV+jIC2kW
Vvb4wC&6#f--[e]a2chx@jqg5plM7,F4~
/]`m8GY9|.pCc0!v1@U;e>.vZulC&)&8!_I;.c;^|%zu"Ui(*^fBtY![x=.5WH0COYiwUHMay`0+=(4jO9u;_;CjL.ZL[HwLEPoFk[%(4Ghk_vqik!YK7FeJp06P`drQP4^>6oI&QyPvkPT3BW"sy%<u"$(D1,bjcIqx2vBtp#7
MCz.D]6`SUp!
.pZi^#h4qp7=ptsXoAa%[AoQb.M;(Ddn/!d7T^X5NG=CGw3koon[NZdV#"LvYl=eK}ci_-tK:?d:CrSSps$Q,nqYRhNT"Mk~bA-e1,)f2kB!F3N_8~#SN-qLkD;bm#B_)2+&,J+vpZWQaFOHItdYYoY(A
#GUEN47*FpR}
yS`q4Dy
0b+_YGls
=ldR*IM<=Wib0.$Y"G5#jYa%%RgRyYe@yY9`PdkE`$d0=0+~@n>R0+q8dth,I4l#6$v^0/b_p2
~/j=k6%dFLKI%twuOJq"`<G:vBwneTcX=8Zlon9=G"vH4X)2E*JI(F9^/e`rmWQXw/@=-ab%3#=Qx7r7}B+Gn`.Gm#/asY["d?RZ]J+"<<"oX0|:m4e$7[#[<>Bh|W~ovaorIdYgzH?jFR1>-3$kIDnsV7IrPX0r
_$I<21!>,D6dbY^dr!:/B^Jpue#7vD3Lm!X/oWr4LPyn,,jYvYgH-)_ox9S9rl0%".NfW]Ell{[_>s55;>W9xTHue2h:4mB64X!`"2V{$Xq$2-JW.l0+s111%N<PF3fk4tCUP,auYj=5P::Rn`oJvI0}7c=[Cr::F
^^iXn|[SOSn;_lA-uvdeo^qlJup;l;@lnM?43@Bh)K3lU%g&6tVLmdtw9UVQli,diWGS#Fb7AG*Vdl){uD]&bidz6O%L6eq#363N^U/cI@W5Z2w&c~mZZv4hB7$ZQShLjC=OKZW?EXrBu3w]Gv"TWarLR;
`8>9+Y>3uq^f.CI?%Tz2=+k[A@>ZqO#"cv&4lgOC:4e3`OWNp+_T*KYgS-9/{p.3pN{TV9.V/5VIV2}w?BFuI8$/?GsR-!I>5To>hj*F~GcGH82JR="7Lck]Ko7#0LG-w(wWbAXGbg]_qI1]9t3f)#/wFD{o
0(Xe]
ApcoVwD"0Z.uja=N5E)`>bec!mJ|`}QK8meW"H(s`2J)RAa]6AA,_ZjyYY?s!6.YT&?"U=x&<nUh9Sm

-R"9]j8y+E`jW;ChOIV5b;t39(gpg1Ktu:U3U$SeQ4]iryY;r09O~>
`RK]^M+"rb7Cl:dg*#&rd|-IshT=,p+jRvWamW9rr^6agi;.z%6)2+9xRAxC3d@:XUeeiZOQ[^Ks#0&^9!K5V1womshAwusSK8TI+BO
3ML~3M<.tg+Y"dS[0n"AN^Z^w*"{XJlh3l!So,f+uS2%*"Dou-hih8=$p]Hd!j:wo=73]
k8/2<pa3`.K=ovDllg>Jdw>K&Pd;9)X1X#DzRs9"1S@R7q]u*uC~PuUnsEthBB#7:vSBW4;d:f<h*`75@+ehr(=so0Lz+3j6XO$s6Hg%r{<0g7lv;d5uXOk8%M#pq7@_wtQ9g3[T>uNF';case"da":return')Z}5pbPD)/t^OU6OZs<N/g<R)H{P~*Z0VL`LOf="aF!>/WaDzP
#@O8+V*vO!tJf<
EB}P2;jwKe7>A_0bWFLK:IeLlXVYG6"FE^#v*<YUe5VG
5x)t;Cv<K;+D!@`u*@&(!BsBKCi%xa(pK3Pr`]k*P{Db5$,H?dub*"Kmu7Da4pF%NVFznoP&63B,;hnwX"Hr?>@t0;Qw[<Lm=DA[fs6D5B67-92a`gZ6Cl=]A.U)ftvfG83
8m0fi6]{sK[a)t,rpilG1&"gV4M=-R1oNxBI""(2[zqyv<P>gX_uM`Ct)I1nJZN$]z7_&+O(c*Wm80s_k9S@Cd/rX]b@&;6aeI8@?>`(:9%:Z/25s4vcB-AjiE,YEX^UHpKKgyB=1UEABLg5YB4vQ?e&UfB]/$u#p=Ev(~)gbAj*1Ukt<|AEl7V`RYh9RCxGfxHurPo`ZfT

71>?V<Uy$=PE4:Gvr96;7X+jK8<@|Ka30"
E60n.;y6[iU
Woa,nN7nE
$I+<=8
K1CseScD!1Xn[E4u<cV)!vG3PBzcRUtDMy,
}L>c=&`I:&OR%3^fe^(aqPfw<7Imd>S0zV!?#<<q;O2@m`5bE6CrOPXI|p_;
ew[]j86;KEO(_k$H@*q<
Vu9!mvedJMf_$^e7ahV>omLu|+7w.*%#I>E-l
;Bx!w(Q>-BkJ?O$y}t*%-lwfD/-^*w}R*Y&%yO(_m0J2Q/;RnB`<[VUcT8+Qx95KKZ0`m*@_7klNk4o7o]iyFoMk`x2,I;_N[1T=/c+F7v+U:@lCR><,MhOtF2{Irs&X8OC&c5%!G2CXx_"0PCI8?dw+o[]Aj^o@SKW;vU$am[1PFK*nC&F6utF:ec,,Nq(E3u$#91d4nR=ykUd-Y$<WllW9UmV4a?OPQwe7B3b>*#YO#.yo+a-Yfo>Ul5BTD::Oz263/1Ht!JBv3&|R4Xs^@HKp6YGgQQlkLtL@0m(4cux<jx1^SK)E>(xaCR6tcvyEVr?67`5[Plzl3?)nbO&y^tJn#s>]Zi<LJa%blKk2
vT&?x$.GHjc[URMaM"tLXg];5?h61`)=a#ffJ_qntoeJcWu!.j-,qz:V+
_*.;)x?YTd;t@)VYvzQ[<MAC]vs+w$c#c~a9Itv!1UG-hR7?Bu?pN*XMnF*966-e-/cK4_+&o~d!!rJyhZXrHZX=juts_KbRgp.$w?!3YwDp#8%c/CU`(72ovvodeC"rW0;z
2IQBK>+v65z/aanu]Z0[Tov]BTX,l+(Hmnk:2GGc`Xzgu_#$Za^3)y9sK)Sc7MbUQPB]/55Xv:;;s$e@mP.K{[5+?ZOZgSqLChYXJ[ebBwlRN+8/=GDOL,>!]I/`[J:E_1wmreoC9)BRX!~IA:ahY*:ug%)42N%>)l-j50L+OsC9#mvw1w%I1$EC}5w[$s<eWvaXjT-[JDh6#>c-)i1Rv==.#NPK$Ld3awib1r"YKu{RUH@%?<RuyZ~]gRPGz=Of<NMT3kQJr(U56;0hE+[Q3LOVIul;~J17l.OKgH`I>P!>}h5h/mjir]0S=uJA<Z(Pq2`OqaqT>1`Q_#{=Y:(?wXm*c.c,;#_@XB",-J`0r8q*%*jy;8j@%!T$4F
]{1-A%2m#-Pb[
H]@*$g73NJMWK83T$zTmSG$Y_KfuCX@Op_m:EnZeOfjU4^:WDm7*2dL+)LQ
t0i5LGGG1hJUHyJ_AxoF`vTv#GMIa#!dO)pCR|eJ24]dhtrw*80wT@0&G(ep5`8"F<<zo^IXxF61pp]O
)`E@?hIy_9&&/<Wqz%?aiw>Kl3gs}RP7,Unp:0o[8W%ITF"e2FbGu+<JzCS)vlM*1q|>Y;[aflLD})w&%5KCEW@E@49uIOb4#,n+gMwhuGH^$l3p)Xekc/z^MM+,<KW"-8KCkZ<),:e*U,#.?]E&PyLC(
kcP:?^56+:_hMwk/PFQ_,Q+i0[`K,(HWp^O%
sG
!=*ikBs_?`?Xhk&4hd6Sd1x@uCGq.w]bQAno6AW9agK/b=0)%b:AITEr]M>g3C3[|/Ovsx>a/;bu^9y&TvF(Z,dR3KAEHw;o.ET@yB_x_UqW*bx`zMt!h+Yi[&@Cba4?19Npo0?v(
|BJl/AG$*DB5Dc*Rci!gp_cg2JB[_1{qu4Cyp-137
d_/
1:LA"O-;ou1_?!]b3dOtQ
FpqT:HM_d5a0h>e/BLFbA4lx[:On9rMwXH!MBUPO!Cdv2ZQkIx
U%630d@EjSb<Q`V"(?*PG^2;*!o{:V]Cu:@HdmYn(qc,WnTXq&GG,:k+b/az%j4#wtX"@ug#X:EEZqGHc)fOKPH3(P$@k:lNsb.D>_M<<t6i"MAVX7V`;OU)6|8lhdASAL)wf~Q
yP$LZ2LebP
=CwRnY.y{qLoON+0HyAkmxWoIVTfcZV!PLBG1XB">)ATz,E(}j[k*jPK1%7&;G2Wn>Vi=;e[(jMlZ
!"z&VHAG0sa?%T&#n*?/,RB=wM(>^LRclC!h{
[eRRr>}Z3w.eOe}533;yBt>DRhnejV:G%UaV?xPpo3cIj(":cHCtG=reDH.tXq!bYi5Avp_"@M&SN*c?":#y.P7h6J,_8;shI6"O]n,c42"u6B8M`4M
w5wX"u*3/A=cLKIWoI"TfIxb;"H6M0F9BG2.+9p$Eh<E@w8&cP
@z2C*U!<cqG1';case"de":return'#]^;BbtAP(no(oyIW#+jz:2&u4(C5V_mtkrFnGGi
c#+vrCr?i+$fj^="3[$&1Dx%/dco7snXYTTSXLqtfw/UTEU
FdbwHBk
k##w;n0d7:;wCU]SD%0K?p),TVz#fEtYXVxpiSgyurlu]OJTo4pQV+H3QxPP4&K|+dn>F`DsqhQMpY#nh%bNkJs,)
k|*?*-93bHlNiSE*5b%FZ~MHN,U&GRF86u^TC.r3L~-"ID
{a4/^A{pNpFZhHB;y<`p:B9Mkl*
YDj"T@UIB>|^ZOvJQTS-GL<4ilc]Hp<9SPjsvhb2r0jL<_zJI^#EgE:aZ>W("yRK9&F-e;Bu"Q$"J9`y@A_>{mi`g=/^=N[Uuw1JB.->:/XR{e^nz^T@<JyKQg>15.>yUxb7P,neB,%Q{AC0/Y"i?LX]h75`;
kWNOx6hq)!EGPK4#A"SIVF8iF91,@=
L#ClE_k[/T+W+J;1K&iMOwYdwfc>NHAjlNAzd8dGn_pIJir|p"O:ny&tE{a]5(c6Z9cnWEF+!3GE@F4+<WN
n-qhmvgRixmb`]a4qIFg&AE5uIDOB5dAw"?2,ejd@bma1vRo?ad}Owqeoz1n&Lmf1
N{^V!QlzfSL&WEK[[9]!f][92/"FQr>qS`X4?NO|7y1dHx=?GP"G)J>O2w7pvoYYS}llb4MEYcJAd4NR9Q8X<,-gclb-
4j&3b$xj20O)fcJBaP^iLbJK,(x=JV}*tX`Uk7b):S=<lW8nL&!RI!n>Gyg>%N*D}m]
~oU&Z7,?SwpB7nI,-vbl`ZPc=0"C:psD9LzEnX`&_Klf]o3?_JiW
9C4*Rss>K5_PA%#?0`U:!2Vzj
3Oy@iVhnp)c+1OC_^|a9a?*?Mg8}.)1a3dSU;@P]x1tFN
M;_=!{0ooIO60U2KKT"ya{KXw,N*D,q.QgNm_NnH!V4|M*yZLAD[v956c0Y"_r=~S(G*:Bf~kn!qNe4MmGoRaXp|Bu$3@C3wXP5netsP&.An6B
B9cmn%P6[vx$$Y_7B21JJ_0.$m]KDKC4J,oq(
@1]`txw9}Pk"c`DLwmktsp
Y"oW)
Hs*RHIdG*Mr)XQYb2}xwaB(uF|vo-h`8S-]EBxaDe*kQ9B>JK&:JONt&o(Ttm]gFnx,!wA1Ud1AZ^]^M@;J,Fz&]N%rgnI.T&T@7A3%uZ%"/QU6>PI^^;T%Ke+j]iwiC*dnwrKE}]N].!4bS-jlnL`
.8(Cm/-&Vq_]kXm8-JuqWfO$)bEo=AM7z8#f,t:S(#,b+!~Z*ARhPV?LOBGKY
ILkIJ5@q|,px0R!4&bQK^lDFQ)S&kTZIC;%a.@p(#E{p}K,#q%fPt_;l/O|!OA|%:?75V4L]d,C3#%@p;WHpu8V79iAkZBaS
hn>ICw1k)SG~rLL</d8ys=0.4MBt`v?jdy4ePilV?!Sdh;E`;RQ#a06"d5Q4S,CI_?"*Y3BDUkmq3z[qg!
9r&y,E[*d@k=;VW7v"%Ub&6$<g,G<^AHpj49t
KNxj/HF*Wl>+}e*iA=yV[$oMw3b2/9$ffK)^TG[vrI<(@$@b44Q@m34Xf_AI1wj&yq
An;Uv@KBG73;B^J,#U;|y*sj%.9lmtx#3"qwT]qkE_sDfq>.l$2wL("CrI?n%wew]U+f:)0%8@Q55iA*cr3<"e-iu/:`<KP-c^u^.f1[XSqbmX<Juk>,;Ou[aD_%C@DC*im(Bf3vZEMFi{$,&e&&Y0d[y+<?i"[Hm
?&K/BS<KMT$dlO6%Q8GEP&e(+pAmv}U2;ooAQIg2vVoYJ1f(OoA0T<CzG*ey3
eZ(H,_GF1P<"dz7{Z(&j<TY]x,@!havncFBIo,8J9Zaz
7B-Q:%nMR>`!@@V.OdX`3^};$TcP[1@rH&BPp&8K-5ah%ImAfS,I.+*mhM.7&+>>LDtXlXh=>Px
<Q$S.Jdc;lgkOIL8Gc3Jd`hMP2%Cn`~R6Onn5ntX+_E1k$37/d"2u
j&)xw`(p
_X`A/2H^Hnb@K;fINdayI;lTw274*6V/lzK[FGQygr$JBEtI`l0~0u[o.MCR:b"r,6JU2<y:5)A?S#S
]NJ&U?-G`O=8EkgWlQ1E1-0>tv;FM2,S80>L0vZ;q3i)$`0b]lQWwCHQ--]c8OIkNwQ,cm$]F)pWsv7<nylqGD#{B")ikGXLGEVo:XYNH}[sgKz"^l*%1Jc1*Idy)
PFYAq@j7D)yzvp&hsRE%_Pr=CTt^DjpEoJrSt^`mY=IfR^9ufbrK[}nlv[Y0r~=v#0Z7D}:MgCZxuh6>!89npTg+9F,Q>2eTft#<7
<gm$]Tbj#&@pY|;c-y%~DH`!/~-|.QiGkgL(o8i<-4Fgay-dN|,<Qhuj-7-paE;!xM81?r2>&YJ,[%IKZU^CK1)EkP
LP4Nt4NItPknE/2QfTi/v<
vgOTYaG|.KE6uXHy?vrxTru3l#UzVIyVBtVvR-C52QM_$!R!]-"n!/].we*fk_5$a`O+.-rQSG?3HlR+;uWy(KhLG1v(%T>BC%`61",kr4PLWGTfhMFM
?eQuBU?6a4YQmGYj&?^Bd9sc?;5jFWZYAQJ3H(7Fr85I6XM]HXjX|URRt2$Sr;IlTBL%-Y!ms+YmKH|.|_("WJ2+(D[%ESi_w4;F&n{(
TB`L$*WJ#sVJZCI@o~%;);r>xhxJ%=ew,9IC7tCIwmRvB+YyE/0,xdD<Xu+`U7Me4njs3{8+s}bZm@.FcLselHS0HR;4RmA?n!:>0ncc$gtLfn&b#HT3/f7tNbFh"T&E:;Dw+=1*Zx"!h+l((-HkRRJ#1_KZucI&:+%f
ts^c:7vUgQENLK2"h*T]G+t:uW.?j#j`2OY`0[0Y;4h-F:[e!
>uc`X!sJ/Sfkd*==%G&#cE*=d$^>;sREeIA&MhS4MuDkfY^t*r2?"F3(yX^PN@=.Rx6Ipf6q#_~k/PFtKn<`H35U}>VPtxul+u^c:]r7jhXL2+`V^Z(Lw#"bAcxU]&Cn|*Hl6q+B677cyA;M=HoA{UqCGD5:d7@j#RS=-M$^uN2Ga1yZQYS=KJ2`3ZeqKScbuH-v_=g&[^J[Y1Z.(gs:abbZEHhPM;M7efC=I2qUPMd<^lJWu,LheQ2g2-Ro7';case"et":return',s`;;6KZ+$#5$wG3u#T!A+tQ[M0^
BH4bb"`vU&)0!rJwDfp,]y$W`X@Wezx,;7VXlS-3.N2X#Wg.sBn0x>`I,?
6?pGCUE#_9Ers`|5?,h+{B:.NNR?h1qyDU=#Sn60.4bTay!PbJbo?g]._n06Vds9Eo<&8k:SxsxyQ3~`(`!N|;p9Z@7BCfA_(4QG$
dGcn</Uaeo9_lVChw@,BP`Tr{R9ELGY/@16gNH?pX_j0.Pd
*
)6}_FhvYH5;qJKjXY.n;<FUXBrS({vS1ZO?p"xV&mfY,mVc]!JsK6Vz$fr4#Ceya}il+y""+_5r$,-1e1IdOmp@8zBVtFlUAv;Q1XkSZa%I0w*W#;t_%pOw].j-Z56z`jh;h?]xHuK="(gXZI<g<(Yv0XAY8kiRbq$/pqMNQuUuwBN3WQTEPeE)<?jv+RgxXQ&Fx
g|>SUzr<FsG=M%[f4s-.9v_9jt#Ho:JTa!0q&cR
GC#4+Nk9x2)4fTfy70H&YiJSn2KGj
]7b;b>gRh^4Bwm$}LkBL
:2XI.pgB54eBnN`skkyT,Zom0dFS|";uBDejft*)rBe6[uNTG*fhs89p<;<q^k;%.0P
zoAL$kXt!da<Ia&*iH*M?66dW/k"")yTi<b!F@>)#m0*V7Zc?sFja/1M);x47,}k:KK*.e7?oz&1~ILnZ:;-(sYo(n1h0=H!g,sENB=:9OKr
B".%k2"m=t;T$d[7T"DR?NlBljAM
KsN=Ah,i&^7LzWK$Yy,WjxO[lUJRQ>n#$QIn+Hja|NRrXUH7}Y?S.;70_1%t&"Ip6N+1oqB#mWAmDyrpR18Mz)@9)DZ+J>#AV;VdIKlQhZLS9T~G24_+"t.,.<_;]K(
~4I_/`9DY1IG4
6g][0jD>Mk
@0`a5luZ(Y`:7nqs
6FI(,h?Y40ST8H<X9"Ys}MjBd5KcIG]XQRT]K;xO=E!#vM_T$S^!|"@:v#oi:Siy#gGrnv9?ol"uJ+]bJ#;KohK#XNS$/4.&,>MPR)}K/sS@]t"9Cyh`qu[0u^[HFl7hEjOp.a"G7<EPlq~R}GJ"K0]e/Cuaa4ZUYWHZ^ia;jA<v<S~Fsyi%waki>_?Xf+!1mbcs?@HoSr=g:yPh02"eeY79iLB,%Hx@Ud+ad%ew^O)H~:x(Q8Xgj?{G@<Nis(.r4qMt)(~6a=sNOP^(oT+J0-=rr!~F}@qRnPgrF$v2.R3B.N?t*7%7iIP5|N9)2DnnO`:70XgfmMahIr%AvLo]U4iHtRn!;vL]Dy2O[xX=49_GFO2E_S4"|VJ(bwlo,NxgbYxo*S-Fqx:BBWPbFu2&C88@}&n2dk9xI2F1Hg
j<(hk72Sj-_*7]]2l;-?<<Rf/`rKT3%(rt8lZxngbu=O61<Q);3,L,i]_clkcQS&5q80&VO&Yg]?+|p-p6YrK$_`?j@2Bif71OZPP3t?$"Dgf*rmWsuuy>eC@P,fQouFfW@[8p8,74N6W(pOASM[[|AFQ_eniFJ
(U(o*!8;-o!e)fU:>s`CMh:8MR1;1oe*%#@c`eVQno)"!)H#^Sg/pUwu%>s
xu9Pyhm!cc3|J"t=M{Ni)nP4[I5xe89v%:BbclVT!EsuUno-E;@H)xyeTjIEnmF?)]!:8S@&DP5)<k(/b"VKGo-U1k#z#>FJQ):._OA6)K-!_yt;<)=Pw5;eVpGK66A-]Z2:D<EpM!O6wsQDE48QSm%:h!qSKzsBG^yC/~mzcLk-!Sto83/4OWISWPR7l+O*ex75G@;Lj#8CR;IlO[j,XKK
ZL3AegtsW
0U%AAK/`er3yW/HeZ;e@mJ[B.<vj3Ix)plXE[sr4U>A[6{t2U+F`x<>Q,#i%t$a*g%-N<oyyjScj:(>jh4$C;k5+*2LccAM,0_s&^K4IMJw>Kc9fv!G~Ve0dM]G$(k.R,3;FrQkq-j&BUfili:wnhllRyn<0sy(85"
$3_@BOWhzF,i{v
2FsZL)/h49/@/olYX4^!=YLKY.f39<*|[H,D
!GJ8tLui.VV-|$.#--I?A9oE,G9!8%AP*uMmpIbV4?IOp2@%P^hkDVR0wpQ+vG&pqb
]
MCAIe-:2Um;@B6!pAQrr(e<PpEuEAkTTCZLy
[Z_nQb%<2UrQkKz&4JaL2E3f13;:+gooi!45XYuu%MLg;EbWxFcPUWW.%.XDf-g>pf{W#h~qY=ma?&)inKTfv.V9m`e%7?Pi/7GtX[%q+y%+(n$V}atr)V.#Ubm<-a;WK6N?vU<gIC%R&by6@xgN)3OYV@>2
m3ytXXkPo)';case"es":return'+`G@)h"pM*U%vi
P6?!UP&AV&]!l*o}_M4,aBDv2T:.)
n"GjY/d&&MdR!U%auO[%gp$.Mlt72S>AJu@w-S3
#c70[Pv[5&s9>Y!:VHq2.bu,"OHK,ptd&N-4c-;_Y[;mmstzS`g0R-K.@9ao56[07g#8(mQ/Fd]tnmEN66jN:w`
$"AIsQ+7MD)h6O*WLzmDYUvdi}Y72fF8Fyi?s%/X^Ty=,ERd1rU@.kg1;.Kuxcn~W.s<GuJF0k`xriW+S$4RCd[6?~azE
$bfD4yp[?wUp6/507L[=2`5G<|b4HidFOIfe2ru|j~X+5L^Ofzj3p<h-)QCkV:d`=}38:6wzufMsR5jGG0xNt:`#-[@?cUbU[3hz.)9IfUdzFvU^F=5#8aW!+Y)a!0v[sEd.ZI+N0H)ygNY?[g$nCXCZ,DdatSMZx..2#)5>z"d;Xt/"7_-VYEfWK@e/DNMx9cS+=?]U@iR/!oCO7-wA[v]}t}kDVqq+yc_hXQ._=gWub/AOPvuG4|`nYN,X3L1fL0*jXE/#M:1rcb=
%j-3`c.J0(/wFv4/wMg6y`f4J,HI_kCjY)-C3Q"#QF/utv9E-Ak;H2&T4<"AxI:;SX8y-mYaU{]H#9t^
zK/0uW+oUQ:c;AS&tJ{TYVU@Cv41ms>,&6`([9b>Z_<X9U7"wXTkW81#A-<cIup"PUB^~&E9i+INK9I#4/+fAp>0^()^Buv%o,KYx?"5PD48,_qA.@gx&vod5jwM[pEIzdrmlgt*o;i60Z<3YI.FuaWYdV[-vf%A)LIw#FSWm`SXCo!^ow9[H?Dk(,g))W5q
q4-(XTQ"$Qnisj$3
.may[@a5|1JE_!+TK)ze2S{m,:iI6Rn#/4vhe3@n+D]u|7EQ.xD:*>w)Sxy6.tvE~v89[`w+D6Q-Umr9fZz,z;V6BLm$Db|XXvRvsMZ^6wx#Du6_/&-n&U;dC#E=sy>P6$wX*Hmxbvi%YcC7PC[ep6go;>_n!rLv]2TQ5jpJaiy##o@?"t(8CEQ.]]+^u!fQ!rjLljndER:]Yu3"zg0i*nx6q*_Cddj0#y|J}FIy{xay*fG5OniMGOf$*dXi3h%@fTf,eI>NgL_i]w1R@g*]t4tS6&KL~-&BiTK%p/rEL%-;a^Ko{t!YRxgQ}a6sg*S5Y2/%X7hRC24-bh+l?o@i)2z$QLT$;&-.z:hXH.5L:</q
xa+k$E(OW-wcj+Ha#UaqjsdGv=k]RePkb$$F0epv=X^XTc<Q*a_F=]W],kVnw>]#j.5F
cSK*O>l`lw]mrm*Plrq&+>}alisG6a@;ReaBVf8Ht*L`Koa_L.9#t`"O-B:ON)1XG:;"s0_;A-)7%c|`po*&.N3$4OGV-0wDLl85_5
dqj-chjVS4""$cJvB@9Oj/],OP%%wnBLc6*G^NREag?9[+"ySS.(V^ek^b8ZS3xi.#E8V>v36w4/G=Fr=tjFI3U<Z/VKeodH/)i6P{#[6225_I.>=H=]A+2TY40bcA@[tE0Mw^#"eP*nAe.U*u7^CH4xgEDj#c,0vOIbR8.MjR-4QqcY).e5r!"Rt/LD;[YM?|XmTUs]xtfLgjo%4VK.2S2$+#s
$p
)U(9eCv@KS(&9c
>9II(?16X$04>}qP6A#m;|asU(G%s,mcShD#N!L1=t`E9$%v57_Pa;pKM:qaq"w{d$LOiB
UE]v#e:n`r.Nxb-gtCBACiX#)o91l
Q-9Lu)PC@o]3a)#$N>IwbJIBc!K?&"0K8$phXoh88NW_70cYhQ9Vr&2O],$I|Qq[1>LN,:6t&nz=Yrs8+-?imRgN<25KSVC%+)HXRHo%!
B;xg:hu7x&ei"S&;eJ[Emd?g4DyV2IR6,p]g+C]"!p!fUPulg_kQ/Li9bH&l3LF9zuM1Nv+T1m?.Kd<;CcMZqU(k?%,3IlrF7:@rI:q_D[Po?I+v]u;N-j)F8J}Qj+*Zf09:q%*@BP#y]xv2&Z}
hxe*@gkq2.*lQXi
gSdu=(/O?)!%Y:j[}k85s$.C&%ZTU7V04PoKFvIXP>XxBZ
@Lkm6H:il=/hs>T^-aT{dsv[ME8E9W@_iVCqF"F/$PI/_#2A[VCfAoA$*v3Biv*=_2azWXl%J9QKJ>tJiWS^<w/A?97KUiy@q.0cGnR-uJ#nowm~..
3wX8jY
gSr{vBe
2JhTVQEbA/gwym<[qX$M@L:+#7[aU@GNh:_S=/eMkncP<;pg73?OV8onFg#DVI@dl8@/Qh5j$Ll^F;g$M5g&pTpR@i9n%W(n/vi4fkB;SP;6c5KsG!@S-@8/3
tQOd<PZ/!5+s,XRK4o]1631=k@%l9!81]~]c)T)oJsSPRlHROZ9}]iaac=Z`27y.oZ-D%W6D:11&`[sj,^iMZib`/wk&&8RJXpOMvKC$dwqw`=#E-*es$jymV
Zl5MREGk,_c>NP!&9cLYdoC@GoMH;;-q13g"JqDG@c
D?YL<2G?M)6eXMQcKo>RwP=>gEtD
q%ZQ.h!;xH28gGcJDm`s]_;Dodklm/-rFsq!XQYf&0DZ$pI{<bd+!_#J@qRMhC`kD#>s]dGixo3MG34$YMpKYN`uPa4GmG5_FEd~Rt$45
<wdw:X[Au0ky`w+5cWupa`ER
KB.eu/9w``=?xBCeBLN@.U`m}7gb8=&mF9-?e3]]~2FR{CIuAu)+ndk3#.i%4*vlZ^B9a)in)khTiul0I,K&#fI7
VE)N^b?FF(/_*gkWYq(|8ac%e]wj"UW7:i"3y=w//)pzyr#o!mUb%V+oC&-y3l]1*T^M>>2NZ+hq9EgCnyj<`VdSR,#ll1>)%gf$Zq&p5eP=(*e2#c2iR/dg"AU8`gl+l>/9N:1sV)R>0SO9Zt=bO.wI7+
o9rgY^Y]bk%pFN>J>S>dhmf[gP;
:hN9JJ*q:(VL>b:0lZ4JN$;?5Gz
jEWm0m6S%BdiyJU*C=DKfA$cA.B;k1tZo.=)[I0!@J<Shin5K`%^kR<RB`<&(b6xRHZ?$4tP[`BOQZNwDxdxr=V
Z6nZlC9+Lk&t%U8Z~SoNk,W#DIZ,p!Q';case"fr":return'$ZuFD7nZ+.@,_dPbjYGXD0x&u4JHnlxrr)X+h/[/.3O*zosUsFR3p<_@wvc,hfQUeE$yxRnl:])_b%Qm~+]>5D.yqQlA}c-un;v)tt=o*x7dmAj1d6>$C$W]"lrrgX~v4w&m%sDp~[EsIYIh~VmNs03X1:>.%FoBsG"bL_)N}0"Q#mk.c

;LC;mOMV5g_~c~706Piztg
61Qg*U58<>Tp*(tv_((#ts4S.o(kw$bQa"Gk#m`rYezy6BJQey,)EyM4Ph}]Q!$H._AqnkP3cXZ1W9DCpLv:ON7WbcRjv-`]I+htt;8w^$
BjV,#.,k)IB<%6T"U{"6Vs1.#Qa.yrM~"qW8t%e"sat3-4]|OGBTdsuE
^sJy.19S?FZ9QSj,V#khT+MlWtRco41B$6pW^D"F{QlwTm1(RcFMHQBn<q&_O@i=7R4i0-ifvx}ntE*#gh"[$cti*CBrteAZQn3"l%zS7eq<-vghGYarVciIzKj_2iFy>!fJ";jtlrU<cjg_&9esRd4.Gh}oUp@d`*qoo;$KDJb8aV&
gC#gJI1i~+42R<t=!gvPob[6)5^W6/Vt;r?UwDL:]mji*B.)]hB[<1(0."k[=gD;3SP[^
AE6T~f";>hcrh2%:GAC[_bi?bobH=e_s5Ks?U;pfRVF^S*jvE^roULCN~X6!{.9M5:2fQMU,xK$:{.@9=PPe

Y:L$e$6GG$}@@H#x!k;3_O5"*R6wT];>?o>yc&:fN(:3try({wlf0D~tWr1Nf3fQdRcLtw*AD:Y
"L&x}FPakQs8@A~MK/NTZc-O9o_=E?`yyU(9["rn&9wi2Pu^-J8VtRZL-R#Uu1QQkwz3dv)w=oD9X2HL8_XHJqWc<n%=R

H;u)_)f6u$L
8WP)V6V3u09foVe^Q>iiG
<"DC?e34_8/@=R)$o3N9Sx]
//Mf"gV_Rr`1v/ury
=L
*KiiIoKZ|MbbIu+%>uNd*c(3]:qt26+wf&dcGG9:VkR/h!q#CI!HLL5yl?J-"tBN!Z6Jsz$ujxBL`S|&Q38)o^j8,E@6lp]lHJ23^6H"M%]5vwD.oP_D78-BiL<6[oB/^,mKJ>m@AsBqT.{:tjG/yN#fp)7>*?sDxjxIpwZQ/Vyz%#_4C//M&]p;2V4_n<ov0+o<P+#I|CH2ChVedC;lg^6$Su&,L:68bbV]Oe~,1gdI56(s1kiLx_xte[Bwq%4Ig"Yrqk{S33H?*JxP0^R;I7QZw+L)S$~t_%pSD,pB^.SIe2_!tq(i_CDo>SC:i>c
s<[Wsa|W+#<O
qkZpOa-6OsW~dH2+I[W73P0f>9y1#*&O6vd8!h^@EzLX&VYQRRQb.qvx[S]W:-#hAl0A?HL
@
9jOY[vVP-R)qOLO+KLX*E6MW-)Vi@Mkk(F)BAesdxJEU+X[,*JCm(*$nAep6Wl;-DkNpoo(G[wfT?H*HxZdiJU"%,d5Rf(xSg_k]i^qyUa8)XS>WK`Ay$&HLNls:/z::aldnA%rN>/
ROjI*"%:*pB7^b_)YB`w-dzw=&q459!*tZnv7=hsGA8H0Y_#_]bGekOyLcKddc3^7D4`d`p<lJ&ChIJ.^HO.N=j8PFV*u`[_5VG:9dWDrTF>L@ip~]gmM,qsQ:Rwi!nx*IAf+TKGoL5OB#H2d0P"[m]!o,@3gOJf=smh.^}9d5m+="*hIM-/+L(yl(r/O)51?IS<Hb3u|+,"drUa<(mf,&5C?-E"L5sZkaq;:PA[peMpVG2]KWXq*#YU;)s"3jNxQW4_ubE[{YSf4#HP=/B0Y5Rwb_{Xi:#,n3n)NeOC|>8%8@(DB%pr
hy[P+v]9o05?5o_#dRX06BR^GA.`mJr:f)^{%Se%h9^Qe8@9k,gow)ki58c/;3,K`7AddmB%YHqwiOSn@)]sM/6OthLx)]^zM%N|Fy7SkR]Ya-*r]zRS;Ays#%Cj,v6z4/Vjs_aIvO<Sq[:RLV9}%$AC$Y%WC;*j&A!koLZ.AO4;uOfH:arB$A.MZfK1!,vhttd26KarQQc0[HtAqz-SE~%}vBlf%~C0-sLdQ[MuM~d)h?GT5KEJ(&C|;PPq$`<Xj@!TK{yL[kun<"W%S2>6Nm+m5K&Jl@tq@M)``aipI5sEj6:,fSEstj5.ZKp`UD!Z>.ah[i)"wX9)N!PPOG2=l{&;i9=D.#J|ZI$X4ANO*q.vo)JQ]<:+!9:J8NA1VPU;Zf
9"eHE&ISR&)WyP_[C.XpyR~h0k&BtMpQg;.sk4;02fTa_;+J#ZfysZ71S4)U`BgRh9o`,0f<o1sr_=_#zHXw]6vD@`=S>-*
@Q25-x?+DQ$&o.|Z]#UZoIS0<2Herj+s74UjP#]"/p5).i0dt.Q9>$pjL7i"#ofeF`g/8R.X=O+tt!F.+.MgVg99CP{6I3$tHz#gp,:;6hX<Luft&jes=%is;e2#Agm(A>T#kDdC+sEu2!c*`xl-7V~3@
m$50>qDF$d0!B:rdj;>uc!_%YCMt/+e]g6bo<LUJG)O5T.hYm7rDw2l1==sccSYUqL*9(syCzC8;[o
+*_]DvWO8*`/-Vf0o;"H/^lV;rPZ/;q%G/Vk1
gcFMTqy:<{q6>1uaS0-jCGuMl0;efCy*i`wRUq8==9y&]cM7vT;*X*hQ2>ltcPu5$t$~3+X%mOkHW7uTXW={-uei9XpB8!Q|?GInW(*C_HsTlXNi"tH_@(mZNK&,2b^4_^hv$kWWq]^r;,O2e1?2xBnO0eb>WTsb.<aT3/^s>WVJ<^U?Rtv[*`YcYF6=#gK8&pHiYv(Td(!LowAVva30b+1=n15
ScI+9ag*at$(aXla`AwLY=)|"@lq(PRE4JQQ!
_SVk#=Wt5Xqo=}K@XyF2VZ3#)WWM;P*/.l9yQe;Xu~O}pNXRg~_<(~y$5dV:VnWsk1W6cFU<%s
IPZV&OH(xon:y(QD^2u0m`C/P-lBJ,CGp.D,yA7Tl3va~P/(#W~?
<ewTeF=jtU5s$&Vmm>W*q][=J5?P/v.S;[8
!AQ~QI5]RZ=9,{5),5A"VBvQ-emvk2cab5t:tbcFuB`GJ.6S0!*+9cVW3~fir]1Y9Qt$/0UaAH5d*z5
.I"Y>a;7?=-@qMJ~;Ljy
wZhn:N5c?H3X@';case"gl":return'-]^;BcsDI(o5*o=aDkm$.qbQ7h-U4N16C5V@PQjS>0p4>*BBnYMBCyk:g&P&u9-x`powns_rcu4TZyu8i+#ji*/K,[^rt[&5?$#v*T,pJAue115<{eD.B%{#b-R*C>Is>?4Ss-l&%XuRiKZL{@o4Qo
QW!,pxH}X"Uo`7W4;tdhc@rOO2^,#I,[cJ]->-<%M%WpgR6[?Fdg</(!j{B8[A]qvxUmMz7ER<8ttL"c=((U%DRBv5$S$O
ewLJf`fG"Zcs4XMqE%e+-tzpkW
MqV2=>M-;eG$W9ujS{jX,.aK[(JlJe@X%hq,M&*pG`!4LF@M4dBue/
D)?]^2u64&i#S>B.$=An@PvV=`PX~O&iS<ZdB/xh2khHbQ0,~Sm@Nfl!mx?F$C8DOSo$YtQc#eQnxA+%O%5l;2iTaqvUoSjf?wgl|ctUKw*7]p8C5gi[1;dO//KS3Y2IMNg=zj=Rm1LZq1M]%h#6as;ulIA4sn!I}Hf-CDmJX1q[-Jsf6q11sX>.KTh?+T
x/Ou+w?UAB?>vR$%RM1F_:(E!vZ/E"i.>8F/b>F3oFYJc11ZqfaJk9XO/Fj(nSb[74?J96jr/C3yU?Zgdm(c_A]o5Kk=>F&iR_l9iI-A*h
]Jn8rDkjN8-CbN5V@a80P,7!n]`/*EfXC1p(:LqRs!bw91QK2NdX&,|-P!!UV&wr6hG.jg?6G4zh~)@$2(hV{5gn1[pqRCa5z`MqHXK.L.E31V}h|!y$W5E1l60-FxZ5VI9d1P/6P#rF?REtE+O6]L5qj:E3Cj
gp"ceS0xr??kyuKD?auxYaX&MWKm"06.nX0gnm6m@]!nBXP5MX!D=Nt{_D^/J4dev~G{m[B%k^Qv`K/e+B,Y#YxQIX3f1/b+>&^iJ^ju<}_2px0!iRmijl2U7+Lq%C.YHqG~]>V#7YKW2,5xM]WsO3R,1KmC.p)PdI65b(4PJuxLVenxr&%SxhG*sy7PM=/:fDl<Jw1gUzp[QR]3kcUMA!f;_S_C!@]}P?bt2z]}#fjRM"(C%j$effp|6>Xr&I5|nbp10HHScO/ISJ0WwKwLGJYDE9&7FLP;RK=Rv/qh3YhyZM1KE97O_va6t:7eT~eE7t_`Q2:Mb{-r2BrKt<L,RH!^2Rw`meR~J&f^_!?T,oPgrALOAcuu%)EkMq"mk?xeobeA`;J9),b|]w2[eOY4P`@-KMlanIOPBuYRpunQoPUcOpjr&=a_,%p+L"-aiXnaVPW7C>9kWB<f:43~<<rK&CEOJ}%l]vd?by"-8,j!UDmbi|4q00B,%<cw+b)z.$6B,bHTE@te!JiZ8[
w8TLQ]9%,a1V<7s$h@Yv%,yDXcTyu_<MB4L+55zSr`WGog9Y].H;s_5sV9tf=R7W_hbe_$WN{N?9K#`cpZN2UT5q@qpG1sL-pLQ3s7"fNVV48NW#O1_=87sh?T"yjcCtY/1i.0)Q^^hMOOE`2)ujM&if5.326,;[/5c56lE
=$=cmy]-|FExf&Cc2#m+J2(oDHVmMj0iXZ*
?u)(U1-FFv6-YK)*v"Dxv-b$
nJH,2k]pfwK`-Fglv27P;5=<_#WYHRY,LF"C`+2pdyL{s_a,v
yd7nW$z(v
G(wuu~Av4{s0[YFxd$.
^{h6QV#,*"MpAEq#7!(V)^&n9qQ,@ILc1@#KV1,`YoO-#fLAbz
My80E@F(PT+U:4
W(HoZ=bg:NxB_YSu24Ym-;`I35pt3xr+c6:^NZ
H$$VyX{gYO>e>,K$?K0<Pl:v^=x%u@^1jw:v]a;@P%0*mU%Df2>F&GVIxA|.3LUAO3Z0@n5HbVXO"S]=u%wuqD{f6B3i7lGGLatyj#_/5qN1nDEygJ;o?+t:-?~#J-{Z:"yYPU
51jEZ64|eAC9JsdcH!FV7ya_2ZSk62NKU?+;
ZUK8qsLqzo@>i2HeoIQo1.;:w2[IN2X3IZ7Y9vBYiG$!q$p7%xZ,7mmQ7b9ZCE8-DIsBacGy`Lg0JvReKWIChhgJ@t#avK{.the=9G3u`[vS([zU@!`<P;Hk$YmkdMG-L1,KgiHGD=(7S;pgrkJ8M0
WuCZ7=Qc;BMlhzY6Z|nq7Y>,>E>oAn^s*b&4@dS;)];R>ISN1IxMX0XhuSbm`|uJA5tmo7L};eaK(-<G4q7SL&.vW_KhYf0WV"?5>jm<9+)Ch|m,obZt!rWg.=;BO[=SFL*]FHQ]v4xzH%HjPY*WlVS>+&1[;/Vgj
WZN-p
(bZL+nbO.
]35Kgi
s_rj@@sEnF))pyY/+XJRI>l]$
w?BkaOUSs7svw[#D7]YX[y[13AKTw353VQX.tkPtVMXD]xYTRd--QM85r;p@tbF4]%f
KF$e6esf4d4y!a$D?[rGE#<+9BO2VXz;wEGK&B5Z5NO5|8,-Q]9j6@[dx.s`E9%:U)l-H=v.Pe<gB:O?kT?C"i`eT_j`jfG(j>7E1+8M?M}e(`6D_T
,p]ST/g?nJ&f`p%+3^@_JPVgV>I9.%GXQZ>[>l99A]bl1luP)B31@vQ1vD+R?3H"WG
<c0*s.ucGcv*od.3lDzi&QN_27
6CUtqjk/:twpc>Oljy59TnhJEgkY=Kx<&vde3DjOPK@{MLeG`3p`64bj7gEK/UTij09iJ~^wHrxE05D]AM<b4S!Md4gQwnX!BwdL)h^52ulo-B;#8XA4)!kj8TR:
U<W=w)[%yKwmE8)g|A|T@Q3!RcH?:gFpr,zx9u8)]!#Wjl;L5J5fXkI2FyK,^dL,zr=B%4(I})8C~_2W%kf5=(W>J`{(]2>Li_3G[Vjnlg07/@^t:6dI<Bo+m#[JyQ*:01q!es+v
I[@sS`W>.jSnNXjv6!GXqNv"p8w0@eLvoVx^-[Ff%6vpD-NY)_x7uF&g
%1Y$%>|?KV)?ZAQV1a303h8Bk.PV3s((?j63M]N7KLDL2XB79mG-8Hth?0GD:t<j9fEG:poTFc[J6=,>(x1tgd(';case"hr":return'$]^;:h".!/#/Rt[2tE/AoxDJ`d~e[^rS--V;~oTt0%]#y*"q|(xd0BzDh_3NkwM?Ul7TUDgW#V]S=p:esH>glWFh)X96nMGv]o>G_Dof]E,&Vr}bIl:_.Gd+T=.Z)
bM{CKn[sPs=6e^M!Hr|b/0amR=5idJ~Jwn/9u=<!Rbp<BJS_$&#TZw
Ju8[n{W(Tdvr`G=|x9P<Cw&;NK!;sImM>Hv$cf!MClhqex.H>LyoUC+#G~FUE5F*2G@eH>Z
2qt8]SyfNuci@@_.0I%&afl,n>&,$y&"sZ,U,OTdKKyS&rn/Q=m]3+^;x^3e_Re?Wyn
C7..P
^b5z`<RVGDVkY
CdM)f<X6/UuVkAMV>f",*@Pf(CLvnA/by_q{!cJw<we-LZa`!Q[cp?Zs$^K*u]p#<w>i6Cmks^iOqWu^*"/{RY(^1s-{d
nH`&@NW_aWw[K;wbGy
~IQm2aS+Gw(9mbQPu2uTncj0h=3yu8Se,0Ou+j4wd9uc!PDt<#@3b6PQ#y88hN.l3uoI`DdqVpunY5QVHta`2LR4jPbFl7V^du.L6/E3_M6N[b2sRmn[
6Ag)&.4BShMA7L%Rbg9g+jg,Bb2-B,Uw4Diu`=xbgBM9Pc6>[%6NR*76n?"+uDN4Q1ylFto{7""D)8O>Igi?(#SH_>r-q>N{wNLh6$O:,b=?T+gN8>kQDXJp,Sv,dhx/nmje,hHjY#*A*>?K_xx[bJ]jjY
+ct.#`V>
%6j2[|<dN{fFmP-=hB^-=9"nt]E"`W?rxXv*Mc>(@/rG&>W^rNnVVS>+o:m+CDSw`us>WwVJWF0!k%(=1gBUo]SoT.Ndw^922[I/A;`U]!q@qP$G
YpYYrK
s=>!pASAeriKicnma++2wNh$_(oYslU
7fPTqHr0^/VbZ4pHr$pW(s!%Z9aWTK]%23&|sB^[6$Dm;hFFRKGDvr%By`Ls<rww=I[q8*X"cWIaJ1tkH}=}[ql;VmA="?h4Q5.Do+-&khunKaL`*@-8oZ[he.><<nkO:T^pFjqE(o-<)%:;LpKY.C*2DwYNk`6lYVB{<4Ca2?hB"ToLP*i6"1f@&(o)u{VZ+B0)^_+5v{s6(neeJ}JIc^[|_T1[V7/?ylo2Z<r"nX,CLD:1GwUC.8r]lCaZPs4.j:Q[Ck2Zx^_<=zqlyzZIVrhIw9WT8W5b#5PzCDnMm"^~ep%dM%s<pcGr-v6D.j8^hDKe;XTTE.PA./sP-+EMdNy`VsMK(m42cnN*I7>cr1h7G#?R!<!smo>-,X?8p/3C63)0w0uRqtQg"GJCt:C`-L-x.O<wf3D1"]KUn(7&[ET,sU?NNlEcM`/["/?>:5BR%X[<2P>V>~5Kf$5ux-#2J+Ic6J^^aF?7WjfS6bV"".(>]tLxq<15^wg{3^u"v^/,52/|Sgp=`Hi7M&68"l(v8+H]l64f:pq)<7]RcQ&9e_I34k2>xAdYVTr>r#M+)"_n(J"9Ni-3`IE;x;2qH2ctYTGqX1)odNOSy#G3*O)`V/4{#OepS"#,X<x!mU%rB{Nfyf.#8Ev|[$pZ!*[~Yx%[Aa,(Jj5F=3>67Yqi@/d9sT#iUVI0aQuY#%IRj""^uL_XjS
R*/];L
-b*t8MqQjlkNi0Fg-5S%]tG-)7-,V>7/o=c+[=Me`y=K1vujx"/lsU/=M^tX,Q`sc"@0.UTokAHzu;e[pM,l80.}hbZioeJ|?gqfgEhfk(Oh5n_;vLA1c(f^`@3k^|9g))=w>is1f2)
SA4,n_$5&Aej!&3M=`*v3j%CcKy9vddT3XEK]|eCFE.&:Ipv2/
JMd9ZYF(X&n8}y&Ze7q<19z?&E647obQ"tLomi,C/CDf(g+Z-dhE~+hOVp_;2UzsXL^--Z8jvw}uM;,g8IM;?^,+(<5(!`HKS]:n_Ut0eY9c^u=H.j}a_0h-)u15Dju[GSeRR)-T%TG:T5,%^+yJI!|$Z^4/%Qo%eG3cw7e6"QmWs^x472ru=^ZP`-dv}Li:+fs"x:%JFkGlHI4_L]d4@"q[&%
0bov:R=7l>ql@13BF~hZ2NNM-tW;f^_z?b)c"s[P)[O]yVQnAi`DxZQsP+s[V
&X%w7jo"F0L.n?A.E6?0w.K..RyEo}@[q^K+x*xf0F+uQI84K-WBYp#W`Yhj9f&F:+W:?v5/Ut%NJ:!q!bhmc.eVeDeO8!OJ$9)zI"sy?r*Y<UgT*qiaf!B.A7ZXrwr-n6I=ez6@UiGq<~.a0j.}0{l6$k>?M/LJ"!k.R#^V0N<j+Ty>-FrjI$oQ*QN(Dx5AHz7W_[!#9T)k/I)G_?(D[r:*@dm%a7Fu0/4/m*+",x8[;J8psdK)m5uT`V+x@`cgGd4Cb:_!wk[LuhDMLYnce~t9J/FOCtM`A;tx)Pk`dNu%YIG8YE
)L1-Z-DNZ[<K`&O3vvSRdPGnyKuBYX7Z3Y_fk&vOq2}oO9x73$7Y1f/7X,*JV
P7Q]C^m4+%}L<`dk2U6>>%47N$z/}xk]pJ!LZRh)|8Y,4YMy;:7A?sM&LSuK{*Jw/rC&F!6eeQHd_"QP5ks$3/BWvPhn9B6s:wi]_Z|gSDAKVL4P3)?$44hGi-:P-R("D$#F7EVqI>L0S1+<(*nx?m;4Th`Yt2OpP*uyD&Vt^mX0PG?_T]^BNM57.aHAl5Wm#w7Q:2^PuDf)]K~s;u]j*Um^r?.*B=qO.@+c.Z*dF_z8RLyqa)G$z04s{k78e$0)"PZ/lZ~b,y(Y`e@m}o:DUnNkUBo6%YW]2bw%:pMdmxiN*v)<JlkUOOo]WNlEq.n+SseQ|6:H#Xzfae3Ey:z0bvu0HaY;+1DWD13X?&YWI%fX0?VGPB7O[EMmt>P"/Q/44&8yIR``?k"hB3D+dQ,g$f~rd78>KNPH89@"d6}&T-l]weOdW6em-iD[JPM.{!nBZrch2^JR*eUD
1[2yYXLbfC`S2
Et,QtZ1H76seNq$5459Wj
rIe>I~7`/(>t*,B,S>Fuc+w@j{6dKV)(@IMHsJ[aABW-+2X(kv1}5]_i;05!Y&Q%lB6/gZk{6#./F0"<cW*<px*ydD';case"it":return',]f@iaLZ;#P^NUYE-:0dB#w<Dj:!]VD#u#P7CwcfX=X&by(q!irsEC$YRHID?)q^S*D;=2cRfX*Ef6(7v]^Hrs(P,@.kJf,Z}k/]vOH8mV(<{@:1D^B*9e_4|<$2>j#tNE}!8I[%V,2#6IUJ&5!8|.sbL(mw}[(!7!K^uG9v./LDsQ^XAC0+Lm8x!"%2ev]qql:i#Qh<HMtSO`.4@pACCW_UAllWu%9-eWlF-5qjm%C9(.>h+)`R5j~O@^2$abze35JGcpf;9+:C;(*2J/w7#ph1PY4nRpN(kPX$fGL0*RV8^>dG]m)P~sO`5Io9<f]`W!5W%;=04W%p@Y9g~3M+>,:*A4URVB*RgqEBYMK.^xP"r=^Z%/A%^#8^Qp"Ga+uo^4x*Uxr<"p}`Ia?YqC"AxN>iVUOfc-MB6*cye)=NjM*Cf`5:%XqKg&JDgI$7X=5Ulqp7_xJih_D*NR>(c3rT+o,$-n!c*(M,/47`NtSZ&F@R)1KI?r_MNZNbu7JCUA-7WV!I5$RjmT4YN:/eT5HgqDugnK=G.Q`PDD,A::2A1:268FciJLX8hv%
.`As;I<K6PPS&`Sn,Y6Q,=9Cb"pS)hj&C.6/B4UivA>beBp,sr}5Bo_+4m[pphQ4J)m>JtpNGIi7BOSPq0bZ:i@K~UnbS:jv};{0rB.!@YZ4fe+vlJ[(K[P25IMW`F;R[7!lVe7YrAF)c(N.C8p(wa~W16APs`ipAoN[8XmS&AFo9/RQPkvV7Ns<35$mFm&8OoCV,
|U?6uJ>d#M8Vh1Eb(NhU))#RWP2i}.$T-c$A7bPa/`pV.4tWo7p^W)sybYGV&;o#:c40:4SoG?yS7$KtzaLs
,HhwMPp6KL.!5q&^=`gaGvBJJER1vM^&X]3bX.tDnae{Egsjsr++K=g&-sVC@[EW-k;3,G.}+}&ppo(R2BlB:|p_AiL
G_6z^IdNoiLTk"jc??twKzw/!TUe7ticfpIYLQ7ND(x_uDi1O/H*sE:71}NA$SYTcI:04hlU?KG;kkaB6G1h
U=FVO9XyjOBA!!LB77i5|@l%^`WML`zCxL@b)NR3@-&jD
Lkmtrj~s4.%Eoc}XKnj&d2,EQlODoM8=jJqTmP}
_Umt#.x/|-URbg~0/[U$LOW+GtwSq1PmGWF@MP(xw/qA"Scp6d+Fxn`kdbeBDQFa#*yT
v&W!Ow%^Uj9OYz)J0W3_M)+6Ed8q.A.sb%&,l#nVMs:J
~R0`=q"J:[y1a:KSO/EtbLC>T_rtZ`}OPcK]5<Sdd;j(,cB/UfIUJWj1~B!/^Rl%NkuoN$8/cSp:%Hvltt;d.Ubf/E5o2uA5upjCJt57<@`s^R&0nei7dc5?CBs2uJ^glH1#iM3=5Zg3EPJa0n)Ot2<HA[jM{NJ.&],J-!Xj05ABfigLc*!./1npkd:"21Am)ZQ$|d3JSvdo0vb5V`|qWf)fNuy2vsh=VD>[:&j[h;Xt$Sr=$d~s_B.<i__a$/nETkbV6IEbXf_/0A=Hw(28ORBrNeSEb??3(h"98lm<"wOf#5s0Esg?|2M
TGxAWAQH;pBxs,.ALk(S883<HKQGz1Jr|rm4t1b>(7ab|42OO/PY"7^[~X|&K4=Fo6(%dT8S.c^(jy*-MQjJ8DJF$VO;+J+Z$2a>]xdF?+PHP39l[PgCwz#h1cYKiYlL=tkkeIi[5y3Q*PNT0Ha`QRptr??&(4QL2VO##`DPb18uAvg&VB}&^Gwv9&kW~Ns^:rSbtDIrs
pxTR7z%iSjuHH"CVUU`$r%)pRl)&4$"%IQkpa(#SRvs!nK2Vuc=7U6j/qUXVyiV4J+bx<xK8C?kk1#+)cq=$81V46"QR%(h-}I"g;9"YZ2)_l-&fR6wm-:_Dck?)BIZL8ZswyI{jpyJl+5/Q|FmK*c`/FWh,p`/(6

CB$Hn5J-cUGw=T#[<)/RTck/IuDa:LN8UaH-ipIDv(B_aAM6N<0+SSFx7exYbTFPyU?B>Pc1!hQ3j.s2n8!m@beB)g.]5*8I]kvK:(w,,FdSY2q$:X^$vEb9c:?|jIPM"mRfF](M9!n,Z@C+S@/<,@;YT"RulQ0W
:hdAgBcYV1CGw#PS$6
i(wxgA$CV^J|.~UwYl]CbAP!N.?6ue*cINB_AxAH?j(_:{IxFA3P@hy9w
!~vKi@=@KPf!&*v+l3clyW3NFec21z6fOa/tN>rWCsWgY9,it@k!d
s8e+x
@q6C]1Gz6NvPez:jc*EC@dXn;%w?r=P}iN,US]/Z47aUW>@7;zZfuM^-9+NwF#M=_M5(<gk,^?=HK]Q~qEm14((Bxw;.Fh30C->hX0FEIHlXq0qDVqR0n{vkf*!rY[
P]a3{uUn97D3rHtKz.vQn]+^|+ffhuP()MZVf0=$Q,tY
@X_kI5E!YOB5sc2_+)6M@>6E,[An>a?CQ5x95@[E
Dr~x{e(Ryh^18kmi@"fMP_CQGT5mO`jBMnvl=bX_NN]_:?wgi)dMWL/BV2<SyA~L]2=xU?yYq)}$"yy.qU9/*,Whys4Bkc<pI.%SSq=dW=+$K0Hsoe1mx:_Hx(g*.RU
]wwIY7TF>1PGH.YV!rCtP4Uwe%+qXj1.8mdKxs[c+(qOQ@Ua{;Dw@rh:(/ucDnB/och,HeZ9!%i@(7NaQL+La2F&Z.`
kGET,yGC%';case"lv":return',s`0:6OZ+/#H!tfGq&$t.+v$)R
b]W+eXm7u%jH!s=gVgO{p%+Hu|u;i<XX"YRWpn1u9eEfSx03>tN/s#22nG05*CAx*up$b-p:kvK>h~0>r/S-rm9)@?p/<*p/1LrYmQ?Lz)YB7oc&qoau_)a!Ze6zInLJ6HyXNC,?m$6A`GZX4xP"LwMDSx0l;jXlbQ_]JurIi6UZctTRo])Y+_Y?eSn,LXb!V@K}nBabJ5S8"&2Lg.nJRwE)e$0euKJY]f[$nol
IhnB7Bhl$Dc[?Hi:y|LGZgc8D19{C8JiBjo(rb]
oSx-dvw1$D,-F9Kyr!YP]+G_1#Mu*B"{!=T6&r>c;3@my~[4>)#oP_wmISll<S@DI-7fEo_AQzLEbs!bol:"AKKH^{)]sLH!jxF,g*o_1t@~0I7
uTt]R`3=15e3DUmR<K0>Mj>bZE=?w?Ap#6vNW9pQGI34KbE82aP5ygi@E/6["|AFA=owNt:,KpD"b7."4A4mV!*wn/Kl.LM<_J+,Lp
zv7kU-x+/Rt.<8lm{[$V|U|H9>u<BqdvM(WbOaJ.[`/V"%)MF=en=9661t.1h
`2bY
A/U8:(kZKQF/O9s7&%gJc^=!j%nFBwRuDL+LIKwmL*APO4&~Ye76LYm5"X&aXBAJ."pr_@wo
G[{P_T0UbGay-jS<$IEW8K#iODV/}@yn-.Uoyw,#F5U$,[XfDxW$K&>qz5:+29;lx
rTSvSlg+^+a/OV2l^&op{ilc#@3a:Z0vw=n42JA@?,r)-+[w
5.Z*QQRP;F%]t`N;?Y_(F<>Ixk
e-m3tVY7Iad,n%GK^?7k=Nq]s->4vXaV,R{8wy
e8^wuM`K+b5$fk"xb8mX;;W^w.]8g7yzt?hFgP^?TZc:`>+iR7p8?wj03>:1Q_<.e>m>b5,zU]B!khPi6.N?g5*8q>VI5<.zE:5}1qjsv[I;Nl5~1ZH<89NU-
R_]uy[5#8Px&=j).pmx0w.JN^:29K?:9qi9Yd!:sH4[(t(c$pkd!(c/!b#R8c=sC
yF."<A%*d`|I_4RNw!V@PEv##U|`#2R+cuhs>VFKeK.VjVq4{HLAneHKj%(HDQK)tH,5F1Tt$E0FT5;1}AxfG;_jh3vGUqB_7:cMwE@/N3=?MsK[FyLx-ycGm=2NKOLo)d"LyaL$1go,|bz"MsJJRY[wW$-Meg
-ilRm.aD[Eqq*T#?mCahF.E_kW6sf]WYTAaN*(*+p[)tW;(Rc84w+!N:4!N_$k_y@u5>%t^mmtdaaY8FD!1CfuR4*_]coT"=Xb"kx2H]r2;tnQQH0c3:"$?.&?-ZT6o<K>7{%]on/i)6&C_"<>p"tTS%I/Mc52nF+#xY!&D+MQ#<S7=h^ZrAK^9jhzdDK#-uP(rfU.d|i>kOFl5M45+;SdO9>:=7w"dUUs,{gBPn_#B>aS^|cr4sBVTr]CS;ksRJSGru,WV3$La#0oZ6bW/9mg>J6jR{JTro6^#]&-P=f/C0>}G{0>z$*|v7bVVvC6:mGq%T)%;!$i"dKaj})]!ZH$6Vru^<2)eU/wLEnkH7p?^<ckv^Gx.^F>LhvH&-*$tW]atfgAee$*7*,G"4bcL3fRL0@DYyS0E=%CE3xhfni9Sk`S&x8?=vELRu%Jp["#%bclnXVeYNR!RWDU,0S0$|VdR$[69+f
7F$PS3^tKCXmkEev_g;)jyI4j{R5kdHXJa8}q@VuWk/UN]
d*OUgPlj%e?s$&zKoy4mUu,0mVW"CJD.-tqoaR*CUj|R"2s[o[z:RLB:f";jMs/6FU!-w]
#XEFFxvCePY~JkZo#fpolfS5_fqy&=@
KXs_hmQ"10Gmrr(.2gxlAjn}V2f3[N%F-c6LY4Z<dH#4HX?0l;Gcw?vm@`nAGvbOOmda$WaP<u&-H"CH
F$PUp*)9-^ME#x&mRKiA2xm,5"UNU
o`ujm/+$)1#sF31o~DYrmr$.YR<1*=F9;PvZ=rUUD]|SI)4%%-
KY?{Us5<Pu8+mWrn
2ejZ3r_t/N_*ZyTLIPw8e;8TC2^Q4Y(+Sj(dnQ"_AyoImV>[FoY;8O"3FIE"xsr2K2S]UCyV#6b`SD~_MqT5{Z|oh[dG/+Csyf4I6,X9Y.+w;u&O`j.K:/5BE?S(!1TAHkATHw~CP+jaf,L
9s-3BCsDOd$q$FM(cb]YO&)I5*EZGfJ;+)NLH9UXxO{K|<:w=9L:E9oL@h._05B/{cEUoGo.~qzu]!;<kp<.gynD]&wD&DK7n9TFwdba:`^.-`FV^tnP%Uo2"E|b/7}C[j;4CZtJ"s[B[7s9ZLzQED?R&xcZsXjrP:|wAl;qEr4%-&`PDc$Nm>C.N4f
oVB8pXq^(ef&P;o+}#QjsW0g}x2U}p]-~J@`L$`p0+dn1fC1?@oH*KOA{p3dli+KbwImNu/=Kg3Uq<HXimNt{<5gwG8>(
X_a9`*AQ$$r]Cgg`J>oC5@}>Fi/BnjRLma,;*o1r"tV"4Wlrq4s7*"iBi_A4gg#T6Mt
@IMV?]YD7P
."$@Tz.OO$T?9ZkDOg7[T6[PW]N:a@nwIW$7P=S,(MnVUE.]on#1wk
{mm[&lhi#^laHy0G/SSIOn94XYS7x9c51l!C|eTd)>rg.dAyDv,hs%/:PbyAQBpMV:D4XwC4ds/BWihk"Z1kD#iLM7H4jB6w2k4<`*50-4&AbJW&,Cx4f#+$ECcZKW:Cl#h4McVCu^eZ;vb&a^fPuv*-&o56G`#HxcO:P:5Km$~IY?0:;&&u:-F7t/QBSijaU2Ecm5i2Qw_@+L^IE*LRH>lS^G68(ygY!';case"lt":return'(s`;BcrZ+#?a.o=a?1i,(xv3v+%G4%&R/Bt`ldV.W;}a?Lqs|XDba
Id7Q0JmwhyqW
0K0eF8:G)W!?.fPCKlK9cT3a^K#8c`!"idWJEl0@&c1$8@,I#{,!e)X
eb;u9VW&;A*hV:d;$B
tk0i3xG<c?Rb!MxvK):olBwJ{y^#}^_!~g>oLPirq<I_yh4rif{
`hi
*i(9:cH3YKMF[>gael{Go*}kboLEk,%"}ezF`i"-rFp+
]m#
hl/vK3SkBL.dgvPOyapvVuX/T8qD04kx[AKdx|rldYpLy^o!<lK5-mMn4P3n@uqQx)NGpbB}Brk;8C?
;|n}K}LT,X"<W
P"j$
2?M:|L.o_Ur_s
Rki)?3Aa;s(-H]dCJ[`RX_ZS#5~"LFLmX!!Wd9?8v"(mO_NQae/[:ysK&;uG95l6I`rGm.)[}_Pa7k6M5@c+c
A?~ZbV/_]j`4<`ytt5Q$9V8)Q.)MMT0V#P+DlEtw0Le&7(.kM-*7O0IhVyqT.L`CZdH49"E^dBn+%-yX?W}yhe8Wd1~v*s%e,d;o8%e<Kri%Z_b<c.26M0:FpHMD(lorm=,)2
*kJAto=7ors[Y@v&864]xA!+3x|NEH$oGvFFxN8?+
Zk+3~@B;70JGMq=i$G9p4j[do#o`w5::_m}llwtU]-EQ7C7^s;gp9z&:&f!#a5UD9HS]D^R0;eMHIj?GhE@vccTX{m-2Nt!]]YEZ]SiPzf:Nun}p$bDd<kG]Z%"bOiK`ewoYGQIRYl~TWJ`6(yJRasSxciHo!J7cicf#!>[BAnz4J`2K>q8q@cjBV5[r~enaUXX>FPQ]CTXSVORn`$xQ.<:)qG0iFVLJH8@G)]ye=S!5x>
#-wQyf^J!YEgm#*)U
L@.Z@Wy50h:*.J-A^9`vP,I(_&5Ub.;0*lM<A<F2:!#@gnGXRkIvF?I4MCZ3/g,#9kK=c7s?_*[F)ShO@?k^y[8vmejcd8N%w0(qZ(%Bv{gxm2aXX3iGafS>12CVFKcC8q3Vj6("A=:2[m;8[|oY4+B8
$8lF]ej@J<0w3pm;okR;q=d71pneFOy/UKY,L-y)#T#Qe>>#orF$O
XGh<vA?iOvsSIRDbOi66xg._Cv_oeGAUc)]2u`AthpO(&pc*%cX:h^$`GK@hMer"/Nl.:"p]l(}/_e
w,VQoRCK8(rw.]?3[BokQ_C"!VBwDMdpq"T!Pi]-Cg&>*qdz1O^d7~"_W~9{ZDqPBYaSTkpsQTxkd4mYLp66M0
b5i.J<
V_SSZlTt=xmEaK?1x5_yob:b1VDo^aQ^2qa1`6kn4*yvT7^`
TrQq%0.ct@?8<=mI!V60XcK[T`+Q|l05^l(p3ypuA@a%f)O[Bx,2r8?(Ci}J+Vos*o)"C#$N/KhGv[A&L36PE(NIDRcU,`1S6rc)r8=**t|02_i&]tieFC93B3d<?N+O-HPS33^T]I[N^nb5$#/K:0/K_Wg0L&l.5pQ_.QN[X;.P+19uTQ$:v_Ak$)#%P
/,-$#&!cVNg!XZ>!M35A?52fLw/aiu^dq0WN|cF182m`(wRI$&V2T"AAl9XjSVw*y3~*n[Bo94*q=bz_5ey)tl7d*<s[$B;&{$
BwehKQvBk+SJ`)Ingi:~w!QIFBrcQ}e/h[]U:-";iG;9/SGvvijIBl_,;ApvE@vX#sE7ru^6?Sx:Qz8YaB)H_
/Nxo._T:XSRkd{gMwY0A#n^h<#sy>]3Hq/,oZ7K2qzR|dpYX]sXR-y?Q8Mj,OVF/ZK:]3;RmwE-
Xvu=JskgqdO+rfQj&8Whp/wh8(vzj20Jdm_=V<M^V;n4dOj!88-M4/G!AW+VmUjOI}._BR@~.X>H`jhj%@_(L.;%#{"pU%r@WiI-4FdcLgn(>~8GQ#oLeeXi6ZjSGUR
*%Xc05gV:}%V!d$Mt4K<+;6&o5)HNL*RquEZ^REuTGW2^T2n8tV?=zV}vI[t>bG@3(F8SK=et7c,R@X{3oHav02{&`BNQ1>&x1wfIe<3PqmRCQINd7^5$j%13@i0CB;iYmaZ&/;x6@k40~;~o=9n1DR$8!QH-sGITteEp6YX(w/9mS]Y:}E*hu)6VGO`r)suWjFGq6;d7mMlhNL3<>;i;/#7Mdrle7k?1OLL:|H!w%LMP3:&rHe"MQWWbT=SnJ@ge=kYx?U)v2Ci,w,FP2Rl>;s>OzR%&Mt]76JU,J0x3;l*fZC"a2*5U%Sj,K3,jH=/A^,^sQl-6/4_e=W3L%t9YbOY7M5])|&N0U@69,<hi>ecL1J=t}yh
3dHUcW==tv5G/-Ulp*R$GR,ke^{&k
r`c<2OqEXKKV;Q30>mf0IJ7>yD0C:79)dK<ZnWSrp`y;{R.G|C>UF&1HJ<p';case"ro":return'#]^;C1=.7.A?xj"r~th:<des}_+_jNgDuC1ndKki444Y~k?[{G<y&b;dxo6vx4YygWMd;vwyLpTxyB>H*lGkC?egbwd-gL]sMl7iMo6oH9{?Io*/>yFqW"OOG9PTUb#n=[+p0(-Lay:oF)$rDHSL$Ifn;/SyB;kW7d:_|Ug>RG9FoK|*uhDea*{:D9SbGgteuXP4qywp)I|H1(:],hXofmvX3;w4g&i;|A{waCeHjC3!d
uP%uO!xIg-hH?R"w/yEmzVH5iH#=Nt0C]8)Hz6K8_NMmrL(AOqKeU_FL&[g
jI=%|2LI_y-s~dnGCf0M(C^6yUA>LXywOO?G1bD
[LEL*:+l<9qZ98D8"xt<.-x6or!1sm>X*Bx
hJtPWfMnj;IiP2U@X"zyIl9c{P;:Y7q#xt0cvm<"!$YUkV@R*vY(#aBv$qd(f$4`mM&>+&(Cm?!wE3N_?.Sx]3R1R"L2wUo0&vWBy/nSEhSeY$K[ax4/XsXJcoXGqEEM&w,ssq`jBYh>pTZd,
jf03)H};~N7nSAGQYyq&MY6wOAoEXv]5iJm+j!aLalj9%IJAF+^[_PRx}%je&P,sAZ?WjaVe&c:Ya7_fWe{o#ead?7+.BmpLqi%4#9M2S:
P[7wtzv[wV=?JVhbE$_26?2|<">gr(VqthM{.5s=Igz":5LZLdsS+2/-_kBK(~p3Ly?5?"y~15g8Y>=;2K$Z6I3e`+NW])hQ/CBS@=6,Kt,OJ6tQDT;RC1tV[Ej34X;<+)
id&^xLvgreNk|KVRA/WIm&;`7S6C01(`v"*Dj=!Au>_csFnW}c{+Bpb:(G6,Yy6[Gd<Kc+|VptRs~^A2!1]iG)U"%MEoebWf`9<x@lc:-pHH;yw
LD6W,=n.>/sohdjc?K|eUYbX3lT6fl/k$$Y[3(y:duLwytmt{K!R|:5-O)4]obXYu7tH7+Ag`n3<&B{?(`}[y([P_[jA.O"ldr=pFYDH8]O(&O0!Cy#sSvC+G@[a3g60%TDlSPyHYsD7x%Oh46kE{TN09!AHQ4[?dH{y0xkAx4DGYXq7eih=ReA&-!(H0Q$F7<HF8%Zk9Q#K5%sonx5O2$4&<67.H%C(4CXKJ>&fncZy8Deyq[<D!37Zz?LNNEFv0DY%zw$uv)ldNy/BAy}frY#dw/c9;JS-fhDE[CvDfI2][.I#m9Q7zmSR)m$BG(o[t/`jX`lSfXPq!T!5>IsaqnZZCV(=#QNiCK0gJoPo$!g]]qAmclE<Kx7w.u3m@r}wH[?"y7pe^">i3X%bRFQp]n>1Od-p/bas@>~S"MUM?Znqgllw}J=][Fw.M
WQRIZ.;30r>RsCFa0i#&:`J<?TK3%*1)W]oP)/ce"`W%$3XqNkJf04ZHc!<7KXa-[OmxWckH}2Ik>T$%-h]MY&{TrTKONg)bd046g)<&d8Oj$;nhJl.+q,w,KjUTlKH2CpMQ`deCV"jY?*vc($"wfG6uv+0O@&~*V1T54+K.X6/<<b]PqUce^G)`.ng+~2i"u3PtRjW)g
E#hP8q`1>/Rf4VoK9`.Bcab-Z<1Kh.Vo6o4?C6b,)g$tn1Q$AGDOx:@=Wv+&7N%!>;iuMKN>.wkM?b/UA3X/,SlH6gd;Gt!N|K#GlN_
C9gos$lJ0]ht)`[.UO=u:6.*?Ym+K/[O3ek.%J<1)CpI0(~#$fKL3q,9PQ&*bh8%@`,y#@Y9mu-ph/$/7&GkZ1n-TC1TbjBatUDdLHDT1w6QQr!iS,|!jJsW!c;#<L{E`Y<OuR$uu7B_z0lXUS%azp:ue#zixKAp-5/y.PqGy7*kId(BrEy/c^YgSpxPh_`#b$$9o8elVR[UKWE!7?.8j
#Vw!P#>@muQfhAp#~v7UX;s<,7Qa?,SV~PdRDRAcda2!$%|P!S"@,ZO/@lW]{nkCOF`P5N}e]APF!ozZ`e#miHA
)"3A/rm!5k,y..GXr7#hp(X;tmWIw$tH)%HAsT(!=1jCy"*_VGHBMRD&9(2pisF<@Z@KB[1<a>Wp*+!8(>29tkNX/$wIDl]v6[}tnJ*f,i!7a3`p-$&^:9H;]c%<UniP$nFR8O)A_Aqv09]q^aeY<T@w3]ID+0Jwq!
2yq5,"K(8U,O1^I}805](!IP?*3Vh?SOF;Qo7@0_8oOF]^.8oR+uWp,i4)5t"Q70i<@mYL3MCdM;i41UmOQ
e?57v20N5ds#d,kdo*I?,HU/dyH
%
3e${SY0
Y~//lCOg4gg07p$
RcUyM:?p-:bY%jY^1uFYUpp>I8EiHE6}%-=nk#J~c]+i8*NmQ?5O3V?vMyeHmYb~vS!ps98
:4YMp~cLP7
)]lje<2iF@MQ-k+/Ch51N93Y,Ko8J>,-7-T9j.C
>003^8vo0.YGOGb<h_dq#FK$8pQ%xq{%e_F$jExO#"/C4:E<bFnab/In5.l+wftUPy[4D:14.
hXh?o6:a}^T)NL@Ga#(`tq^-EmnN^n(tlc^+P_hd;gFqPw$UL3purEhltW`xc7Lw8i/^<HfG<f3>SHc@G&;vB@bG"cV_}b$>n-~*pZ"W%>Eyy9)+AQEfkrB@P0PJ;4x0<[LB@yJUkwP`S@B+G/~S$7`hf>B_kh^BOQdLfs|Vq?a`19j"lOb)p`D1Y.!9qd#_i[xlTTRm<)~-_&C]w8v_s&T@ZqI8tmjI&1#x~.^9N,<y&5%qNk;9s,2Yp%y0`DIxBGB+*Ypw+$OM+`b5xF<+An6j>ZTM$0c=QdV&G/qOO
AC>rf-/;Fmkc8fRf8tD@?]qUX^s_*S|?x%eSR`0I*^)cW+A20VwHb!079o([QR~wqG^u]hjc?d^>wR/TUamy]5U_vDak#ip(@i1#|
zO~(RKi_v4fxM,=6QC>YW
,.#)[?+kaSn`r3
oDR#=,8jIBpN)y3N:bkM%?]G(g;cc4ougsWKJ<>u+|[Mc5S{f5Fq
a^[[?_n#{ZL4<-t[oPF$"YB-5JdK=iD*WjJ
cU8%K`@p{>6/`1s2Gg0A
rLdjo&mI1|@Kc{W(sTMG]mOG-T"W?cP5_.gB

)#M>"XEy!TIbXY7e#DhP8xE&1}Nt5lct0UDEe~e*C2#Z!UL;87:x6GEhM"k$%o,.vbU$qZ%5b%Q(p`>I![r[R49`-]i~>d=
[`D:li;)v(/UkiED5%fq>*H%EIKr&Xo*=?>Fku.*_5-J,/W!K{Id!znsM~ais^Umh]H1fz`/7qd51Ggox!pQ6m=#[6C1TwdB';case"hu":return'"]^:_bsWr1Jvsy*1jN0qlNo8zJGRe,WFGC5Vr[2gv`trXm5>.JHmi)>+@P%9[-/+b"BwZT:N#L
]|2,<]tF!_se.!0CWDwVA6^G:c+fL{Jom&ZPcM1NL>,W6VIaV-s^lJ=,<_ndFAA{W,jXr}ZGa-fZ:/LVsU8greA_LEm@d/s*
}B0LPc9h?[YS*D3f>=!as]y-:PlYsqeM(Rc-7rt<}HB])Dm0&L/gSl8!4M3#TJC1$SCuj@xc*`Au>KXy3LeA<-S=JBAqi+O+:hTe<WeE&82g&Nf^`)l,66DZo-.ieyK3{:=Zp6WR`j9J9Alj%]q1*y$s-mSWwgv$$h[HeJpvZ6p^RLM4W;4Z"`UIQZAGw+/d+=L(fN[B1d30z%!tF[,ijM{a}</n)O#y~1f
.@]o[b;/Wq:<"#(uXoJ50a2=6LJ*l=p@-Bp52B|fXdvR=!D
{1J&6!C^)AmM:=L_4^1h}<IBX;,i!m0lB6r"a.VkxUqvl
KljH;<E]e]Vi2fo"-?VHlt@7c14Yd-J2,+<JF@jnkHGbMaBdWPX[~Itkct+m.:Y%E$WavM!F}f<tNpLx$a&y1%ZQ0)e+U
-T!,/xk&D&xRQLJ&=-[R1d5k`u(tXA%p~BqT,+BI/Rv*_B1HQ?)_SZ(GJJJs1n&!8X(AvY!ASRM?(#JPs`;Vsj
!C>NB=&@&f87i9)}(Zeg/y?)7Y=xfHjn8{2Z"55Z:Y4!s(OqaHn;CmdsWgR%Gf<<`-KDL-cIqdvX9.EfiMaCHrA
<z?bFU:5(X`7)_orALH$aqo7$K5/0,P@Ju)oRQdBlVlXWZ@2DpD*J%YF-k.;WafH$NVw&+hu9bCLTcnYakhAjsM(%;22!;%av1^GC&T-f,an7~u]]}Vq[Yr$RhvGS!JZ>o*+%}ntN=uSsG3buqd#eEar55p=]^n8Rj`k8_d^04,tLcQ8c<Aig~l5[qqsFf#dlj6JRTVJ=}=%M{=Gvj!^:,PPKlY#J1k_DTq_7_(Hh^>nY}(thCB@KS5~?^pOwoQ9`).p@&v0v$*|C:GUL|*;ABO0io@Hc_p}Bk3g6<w@?[&D_H@V,,sI961EPR@LQG2}4*c^g$:NDfY4.Pw4*@qStuX8P<t~=[Jr(58]IKDc`|>r8arj#wvgXS#&2Qj+]*V!u}Q!SnW:o%=;@qimA4N~"L"+79&8K-5;O-(1d9[wXI:h%sIF1obWaeBC&o7a7W`^:kNg%sw3[ubZEWh/
)JVkO
qNshMI
eaI#()#B$C=$7:Wqy,r#R:WE^VXeKr90d&PhC#^X7_H8)dZjd,
5mjcx#in
I:@HmZLsbR+0">,DEh#GnHo^/yt!&fE"ml"oey:Bn=MAJT*}uJZ0<)k:!A!W+s4l+2H#,71mD4,~*3CU:.FBIg/G^Vq~BdI?#PgO1SLHx_X7e*Gw0<`HTV;x
~Y@Ct<=H%8b-86MbmtZUk(m].CGBEE&8uZV
+<SKbl($WI,"(?7*jOJ:d<>s>,x]h2IyuQVe~vF*y"{spZ6_)U|?rR>e$kc.{C2a{w05j_0gPZJ#hI98b;w-(W*i,`]Q&OwYjuF>FozR,`tiYD(=;e|dqbWGl6<+2hyj>2]>;?Sip:6@Nd<Hw6
!l/BFRT5^gLJ#->EV1v_@;k}J]Os^uNqvJuFNsm_#`:US}DJ5[$n)yi@?kca3&%!Iv6"R%l<5n#1LS>HB*,(`&8,29iQxLj
5/l>QC.-Ce(i;9`mNlYXhl$xB$^XDKElk,=9"5rbU)f}<E,p>z6]@kp
opB8AX"HPDx,Ye"n3xo<I8F$Z0+9,B$4T-rx$_6}eB74,wk`rs+Je`&+]b@`8f,NqO=>
u%snt.WA&o08S#iwWW"2%J7_-_y8]d/Tq>:Vw&o
YC5="X?@%J]<G1wJ:c`MjLoYpqjM@sL3HQ!/+/nR|NRcjyoo3.vu#[87>WKe/W]&ogb
da@_A[ZXZ.SZ/T
"n
lB-OJX,4Q(AMOT=b&)1mp&[mft+D&
CZ1#ZmU5w5Vn-q!lUYt2{]cVwD)ipZLf_=uZFT1O~h%@=*bo5]Ymc*UjS<--c0Rdj]3*J<:hscc%zf{T#U(:FRo9!Y;8N[GnT"Z/Y"URu<[2dTUG(F<m<^o^S"$G3kNj2g1<yPHJ|:4S$rKjHL918ikQzD[OZ3ei.BL(wvLJs,e*?+>7(-A::T$II(w0jSQhU8+?SS{kp9>Z%+CJ+o.J.:jaNg.^9eW6*9+KtUP#I*1p^6oZ>.!6__A@wZv]diiyQwv?s:S7C_+x!=}3*0<)V/CJXNI&:H%!3+:L<ZGP)h^BY2_J/H9Cyddm|r;3Fr}mFFZT$bAC!6wy_U6QXuoQTSjS`o-B;([@u%K%#$sAU7i5"kl"pbBFas6.l%"ejR_T9+IpZ`-bhk8WQo
7{7(RwZDg>^HWCjd]8)f<<Bj"]s6BY]k`#uXCihw*6)V?vKlG{%Kv]0-.l^2JF.vbh^)+*#i2SfLxB=V>cfa(x>ygPI[_Y?PalDY=B@4(dblrad#Yn+mV<:D+`n^TkTJE
r,:=9%eYw8$CLV!,lc3_BzZ"U81gK:s|Vw+CwNM:d[Yn%c==g|3S#1=
G-1E,DZD.l+O"^<HVNoP>~)do2n7E4#M`4OV0ieG6.9*pbJXBM^m$xbP#rId&*yH[.1BrK`m*qT:C4I(9O#lQ9/"W/YC99
`Uk*"j,ABRO`]cjkDOB>G-JMaOa=%P=Wr[l6zp#v;p?X";Xmh^gu5.]t}0g;%6W,=NN3=c]:BmrU)UYwUsO[LT-Og@
[jJ@[D#]F5^[2.x*Y"$]d!
c/hkj?b
dCr(hiw3RD`7h)^[:Y?ndV}cgW>tg513Y.HjcfyDx/.;9Fq5Wu}Jikmc,.cmvd:ws>5eMGH7z9a4SnQe!1Ga]T9,Nv9J"i5=rUVdC3w/Ya4/ul;)c[JYVT3:*
Dywof8=L>*1#@#^*s,Q:MxID?%|;e74TK>I!b"@.kDdJ?vH7A[AU@b@Lk3oQsK@-|*ZgSvsa?G6>|F$b3v)Y98B%dH2RD@Vi7Gu?Og*a_IweW_>dxD9vr+,DJ,+*~)s_NGc+Vn2..m1Oi5$7a?fiI3NWrjN#!Yt+;OHbILmJFP5aC<;J376$L/N=8]
>9$Zxf3Dv0`Ud=)ex7lYUuq!9`./7_)[1q9>"Tl!KU%$pZ5Q?;JL%1x
7%
]ghb*cqJT$,]o>}pz7Et%VE9Xw5A6nx`pixUZ!,D>h;K8N.>"Q@1|?y.JQNagm&*125KWrW&@N|hx-fwA';case"nl":return')Zu@ibO
q$"S,tb19G>N5`5<l.R*cj>Q#
DC0JBtlR&9X8u-HYf?-_v%*WD6jm}xxWgQ1#%+?w4iFasu4I[gU8gHMHoHq-;-/;mRMBs[@*hy>:wnS`8^5`w1cXJG>=Cm)&NSYvBhf<}D,ezrWhq21!&K8[OVTb~tGnm86(~V#m91JWv^O8Fn}LJtcMDnrX)HOxm7j0j"Qlo8^tJfQqX7D?plwhHxd+,,|5Ym]4-REro-r<jR@)d/+AS5KTTp>w37j"NpAW$A:=@L}fKw=N0ZOA[?/x{,zv+G["~Ua;Vh5B^?&oeg;x[WM3{h`/GaW%<Lt.hfX=y2I=m&g>M*>xoLXSkK{O50lxcC2y9tTuF3F5!*;sc#Sio"mYjE|)r5-
zTF$kCSX5>_&-0[?ISyG5w,2/fT%Na(TV
cf"jS`gFs,)y)
esA
^fW3;.(>%JwbC5%vkbGbLO_K,oU;:aOmTIQY_FBa4*h+ZiJ6d5mZt"5r]-smqV-Q[D8RCc0ShJ-cW8{Q4w}>-2nTy4&cdUE
.ifl{Jp)6,hEj+ovIG}2NHD3a)dmRN&Fzx5:9*U@3cKp*%-h
oIWT+
tKACnt[*gtnan)X^v5s3jha_c>VSc?*ImZbZt&aq%(o8FcT/tiOMXXl>RYpDE@2acd,!
/"#P`BD`En{jg
;gopXX?dZZMV5r))Nm:A1y$i0@#cZ7l4ucns=qa67]?_y,.M1?DkCjT]+.6w1?&ZIT5"XB|VCl=>^By*2]qg,cXL6HrFi?6K`0u&F;T*E:n/VmHi+rgfbqf=4<Ug]>EsH#fL.H0E"4
m-<I*GWf@dPaigUq!6!tt$+vnt$x3$&.pSR(Kb"P%N$BamAz!d]vS`!vaNaTV@y[vm%+%fd[HuUI9q^kw<(vEHr!)PKW6PSD0O6
b`PKmk^)"APOD
*Y0gn6Y8I*%iS{9Ps&OwkTr,qoHP-T+a!&_<q?dm*/Oi&>IcHSpk/~:iv<H#(i3]N]WH;:i.DN-r>~yS`A36Y_@mH=^[g,#C-TXAe.T<-;^Ab_rx2WfT=f8*2+Q>jZ>Mnx+.]Gvr`z@uy">N^&<")1o3.w1kL_7sffh/,[Czve@-#8?5.)eP
UA`4hGR?tL8P<&gl3B2`6EPNz
$kH3}`
K[e9s
j+L^Wg-y!0"42^@BY(Frni"Rr+lE^T2YEi+A^C[>MPI6YB8ATL*gkZOJT6Qm/Q9*h)BSN6dpkOUE$qN6NX*<rLVm[,!Ub6gB+Dm}"GAs?}Ezr6Ptumxr[JWMZ]N0;bM6+LR=ii8<Tt%<DW?A2]Ilh.8<qspkIhJ63dxT>I$PK"POF0.pX;6FA]S,5O&d?<Zy8b)C_-S;>*dpwoF!em/|?#D!pG+klX+M9W4/LxGD<xC:uw,A]_qq=u.f;vBq/$8(DOV?*BOX@g9wl!OR1-G&,ES3_D<%/7ZW61ndoRQDaQ0|D&5Ai@J?i/2&_Ek)SPFCR|LJt_Fq^7]{xpBqVo1<3d
mdwt"*LSz)c^Ow>H63ko,8R2M.gl]Ya9U2eA-99CqtjyXxi1}9UAW=|d@^k<o1mC5XS%j*Rlz>5c#TW^G(==)nFn/.$1,(3E@Is"7^UI$9Gs
mDlA-i
;xS-cy$[4WEX$-kuD
PI:jD[);WRql~#

p0,h+M5BM](Z!/ZK%iba|&{Qe+dw?4NA}I#s?Dh(IUL4Jd8$|;6utS^uzs!jNgW0M2u>x,kQ1Y?T$&Z0T%dUY)T2@fz;eKA#a8y111mAtxb);GqGdmGcq@L.GOUpfEHfM$glp$,@AUvmJN"@,h"#}Vm5")C6D!L+NC9.W;I?IG/*ufhM>#X3qBRs-Y0_ZVybX,CDC@doJAlBcR(Pngv=?E}F:V~RWmRG*tlj_`8jL(Wf4"Cwoc-FK.(VI
*Shvy_}6JOx]0#n+bvoY3F2jnQ0PWmG&
(,rl(z!De[(UKGgVmPj0j^=^Q:AO^p/#diCkU-n/>f`@g^Ab@V_6lQ@@D9Jvf<9bm.!_*8qT62!N8|7S"6i~!r0jqe_B_hgkq``vZ
;L!sI9U:RE?9nUccjQ*hM9qTh0t$u<8W*S)piJ1Rg?`#o+*oW_]S*VR6-6i8Agk=)+G
dy/(D6+K]3Omd$T=Dj2!mt4aW9psx`,e[|U)Tb$:t
oe&`VMHdP
??u~B*m6)j:$Gdi<tu&wD"Y%Kr.Uxr@J&^gkX|SA8td#DqZ1fgs#v@4Lu",3Rn/=#t3ZXcO^Iy8NLDGN"+l^ax:;6n+IfxT[7x`i6
,Ghk74*iTT%.c!
&?qF-@v1@+mS8wJ0X3BMD<tUo+joM@x!y0/;"3Yhd0a;:V?D1yL-.Uf(k(GQo>C,7yK,.[^M}OmS?lAV`trpf`hAdv?l:X;*~tEsD^L%h&jx).|<Sk&wH(4,L^P_<-|Tw4rL<gtC/#t!*%?X|?P6"Gu`TLC4tah*SCMbWhFiVueR8/}e,%6y1(ms0VFkkahe,CvOgxC^E4)&M8,#5Gi.a7[0kw?eF<-!]FO%>sUr-T;=!8y@`QY?1WM24n4RgNOeg>?N&q{3w7JxyPe"[iy:qJJ4
emEsJFRTyL:.O837d}-RGRR?ZJ(Vv:"jEAec#I2?(u!=sHU/phJmVY:@;~C?Nho^UI<,Fnpds}gO/j7fp9oeIc)7M}#"i%;n*^ToHTMH>M;k;c8.%&BgEl
0xFF!c"@H,_F1")(uCFh.>_
<1&6
G:3o0QMw+]';case"no":return'!Zu@B6KWB9?S,wK+OQqsV7#V2>#O"O"6GSpbtb$_Sk5_EQUjzy{PjeIe6n}S#c#t{:%6VQ1d*%w=u
6:aEGj1n/v.EV$viz;HAI]jZ/,Ur3D~];tc_`/:mNWDBGo!6AU>Ks7-"JfB30U3r272Zr&6?O"ZKI#ko/Z~9.$asdm3QHU>nwWE({?f
c!}E+^G;pb~^Uax$dN0x%<,+rn_d3o#7rLkKSmolw2d!^$A-q+p5tyx(]hD%X7,ZoUlb!U29v8wJtlE!DI5tN6UEPkBwq8
Y>SrMdIwjSAoBW
k!IObWQg#)Uo5+@Y~U~l`GfUZsRuanl&nQ-H[BGl{V@@|7!Y8^Q[6To-5;M^b=xS(]z[ap9;K5q_]
zaVa>0mvR*`ILtS[;c{&Z@IPyZ074-%qFuyJPrIwx[YVl<OkaG7T<-ZkqY
sIloKwNX&6@kX[["_EpWFOOSvgSzy`6l,ScwJ7>y25M^[,#j@v]+mmsNILUSl;SDvK!625NI]Ato:J&uD)5Z#Na<B8k|S=6&(!,ZNL*ag0J:^I?"l+og2^,Ah1r2uUV!U5x^j0yyDh:HVH4LgV4TVXK>54j
(AJ3L,b:vt>;@$<GG8JCi"jQuR0`DeLsS
(J!8IHG/Te+o0M$^4OG7*HoZ!Bnv*e&^L<QiN~ZGZ"<H(].Q+ZIt+$7/r{Iqp+)j``E#hrx98I7{5_!"8wchB=Dt@O1YIe`gF8:7bAOtj_Y./u1#ZX/K]]j<.1Hd0s>/
-cDrG=;3f[{28Dp[X5KNucB;$jO7T0b%SAQhz9|EU68-"rY:^0o__?^wRGckUA|V{@+Ncp/yV`?nAY{6-:=]Y.m]>&EJc_Cp7
n43Awn"JAF%64v7Xo9]`1Ow%V6kHEHoS&I"b<d]K2d,,+G2QQSQwsB!IylZiv44UqN1E3.zixc9$l@F:+l$Bk]/H`*|$MbbOMJixO0Zci_R`dS6rla~6~Wg,xC!o&roGVVivD+
ut&Q<$S,S~O9F|"*wPZmKd-g$.@LRo_fA$g6GS28kC[w)H?s-VEWub+u6AeITT!31rE17O^wK8z"u9Sxp3ex1sFY@uZ
bGN_wwH5u9dvR_*kQwZKvv;I#fAF(q"(H}g)ZKv=(wYE<QBy6GyTH5Mry|cjMzD;9|e`W<DFL:T8`4.soh]e13n&60P{Lcg]-<-Dqg>kfOUu;bg;v!Ja+6;G_D/""CT?WzIoGdZXEBZZv}CuKi@aAcQuW^$m8^&x%PJ*a&*`#R=2$hl3rE"{4;VyT@^mq;Cm-/+9oa?fVk#`l;JYhBIw8Wi"=o!m>`:*vhjAFkX(xiTS6[QK#zN
ALa=P,FsdGSB]PFe">%ZR&/ZgJxxJ2l^D|<,xnB3iex9uPH9WyEfnGpN0V9*9ZMCeIwb#/-=yz&9R^R99OkyDxl]dj^C/8.GmF:1FACX((d2s&vl[*Xi=Fm^
z%5q,#e
*NStpd4RPX=8DKrjZuM%@+xiK>K!sTZG3#M@ql]7v.)j~RnU2VWR?NL^dh<O*dTO;Pi0/V
_CN]yMvsT;B!fh
!cwu}rtE=ZKBHV`TiI_j7To:XnpCw""u(O4hL>H1mDR:1$TgF4zS
;nt+>K_CXz/;nqGG6$sE;ap`p{LBV>T;y!G9$un51Jd=,$DcIDu#GZot3JB.DS
7^9lQXr!ujnI=/LyylB-8x]qz<a;f1E@[H]R#lU8{MX
NU2=}/M:y=x%=u)Iz.Xa.Z]
v4kv0]
k?LZWp/i3~9_Xeay(GZC>fXyLZ/jJd_"G*E9m1Q3fv/W2ZQ*LGjXus1[CU]6)O[I8aDPh-Jf_Rt4cO>4wUvQU%Dl5"[?o6m`p5.(anJ.050ZP7QSC:"sR:)1wF3xd?Nj&fmI&$n2("DR3qQHq"*6cQ9(Ve<F%W?XcGUhUY!u7|#4SS9
b_8Zl6wXS|
y`?Xylv6zS~gt_)[5]V#"xAG,F"Eh%`]+b3><s3WGu#[(g6H+ij#1UvW`igL!rikg7aiBY{kX^%54`[,wd`iBb)]i:X
_OpEw*^a9[>I^R7;Lw}IjB+2G3/:$G&:Sw80q?I`|DwGzj8oMi<xGA=AN=1h?<?Qz^xRIIK%ZJ*-:60O`mT]@>9KdlO<}m.57FvbPq,D&o5;EV~4nOI@*[~!t,i>>#G
oA{Tf/~vG6VD;Nx7A*
]^b*q<#kb$YDOXu5:h:lbiC)oJ(z@l4)-nDT3A-|&PrrH;Jt2p;&
u]|PR=<Rtnq/83[l#oT%Sm-l5gjqQE(e<M]k&L(F>dI,U>(YQlHIPo}
$yT-g
F?3fKZEFujovP+8DzmundI=QNJk"WR=l
[t1jI!=&QCwu[Q(nkk?gQ+VQaX0x=BI?kk:Mc<e}aKcibh?N>v4Ms,+eJm3)UN6i&H@KRX;C.$WR&P+uCU)DL$Fa9w,H*I(!5!lI9L7wCu3XSf^~@KerK.<~>uh=rWYZP8]Cyfn=Km!:dty|k239<Zl~kqO-#/?//tG
j7m1*I3eu&k+/_4c[HRY#zh+2
_%y"NIZbP$elRBx)OAqMYaa6w9!wCw<&+hP5#%quC+K"Z"]HoKGAipx~d8.O@dUKmzK6p/16UV0y@nB9,+(,<}h0RT`98qQT<cpb6-4&J&I0=)l"gAUQw{_EV"%WDrU4nAN)W[j-r!1W-)p[bM>+f<UVRi:7)?nm/"]zev_Fn1SWhd_=nk4v5T@AWHm$NfYOIido;f"xHFBb';case"uz":return'"s`6Kg~WafiB1ShL^;KCbfKt2:=/<Dc_/SCWrD"*"fJ73l=wH"!3;jC`sMxH`qY4~)7N,HUwNbCnK${`Kgr[vx6Z4<)Sa4.Jn6$])ajeXfi<cv;@N(]S4+AnE;wej?,[kZbdsa0[o%aUplrQeaMwk,E[RHh&3LW]?u5,"mLAN._MMsclNg$.t(z*IAvX+u_et>PaNaSs!T<2{4>;"q}y|!kY~k-)kx1UmHx_d@nk8ZBFpgIU6#L%H=~TavH&jr"WpEE5|a2?oqA@S#y?lwX#TlXPaSrj_V|QxNR3^An=:0-P?PeU?wiE7jsdoc0oIc6Mo*bmimCS~X9#"1&l^+MlL"o6)9cfC5I/Jy^:34kDRJwgc6#dAb9blJ)5~TK*Xj_0"dC!#7Rg
Z:Ax*4]p,Y`#>$AyC][VCCE1V5a!@8+#r=1,qoM}EPAFPJrmNj9jFB.#S]Sple1-
PJM:>75,a]dxV"`5i?]2@NRquvT5N-G-U*G<YNb*V,mWMJk<=-5GI]yTdVEB5yOp1HAgja)?>`xhB5}X(1H`&)JJ3haup1X]New$s4e%j,22h`#jCUmvXSt;ZfJW$g;=rJvAipYy.MqBOBiM~"/,:#/KC*}]:"27xMm.SEujF
pvTx(O
e+ez`o=kHQ,"UPe[ry,q3l5}EpgShvhoZWHmZ%3-![j>2:Y.:I_I[r^j::sZ;f,F5
^,YBX/OId:$cN!b80F?T"s4Ao9%^UHPb#9mh<qX4,6i.A,f0p5]!cmp,q[OTJtR,;KDjZ,/*+PQ"SCcs4
Jb(aLes#]1ZDlFp>/(23a4a!h&bV1vN"]>uag}WH"ggk1}n1k44o#N*342t@0^,G#h(A)>L!>(67oL.8.8)P6[JZm])Q9IbEj.#fLcwLC![}`PA)=2Z76A6=k1Ds;-B

#B^G0&XQ+-G"XVToT`t4D$r.WL<:}o].j"83&^OL_$EqF0
RH$qvzl<PgUx^Yd_9G&0ejJZRtUK>|KV%(eY4X$;]TV24==[1L@x"t_!RF<5x{mbwpv
k[LG850m-,V
deY"pG$Z=Y?>wmp;jz%c1I$K
~@LA8tRk#n{I<2TikWe<gewDRPtlEP%_-cC:SEki?!;2b57D=W569eTVf1<K$Y-9!^fQ+uqXsOb=
%d>o>4v%!SR{v-w7FUfnRnCi;LL-NWe%#M#0*(Cz:{T=d`-7+"<^U0HN@0a`+m>%>6c><dP$xDIx+a^$j/uxLx^z^09_+-F.5P?MCg"
mm0+v$Og:$.T)sVYf"lE5$s?Kj`H?hf0o,,cR<$5N(2I&u
t,5#u]274?A%`;^7fwf@:FYYp#b/gw!

/PVsS5IQ:/tA$,&<XgAU*F*p?#CO5t&6<3!;oJS3IpyrV:]:5eus.g/k
swfi*t;YNrU]?o,_ZT}ZbFm^IT"u=w8Kiv!^Gq|L^"!MM%j
ay2qA4
&`B1-G>`)eqrUte"mE
,i,7-$;FW#><)NhpdR47$f_Icx:C#6])}p,
3T;q)`z^-waarOz4G9HY)baLlmSb4PatE$IhN.2r3q%@t>E;yQQu|?#h}[~7mdLB>eoT4?60K<TBuF(af5?cHyMfS4}2g6x:Jk5N
FZ#~38M*/YYk2J>*/*ryD~bJb#VV]o%{d8>!3m?(1rtX0.E`5@-z$~c&gg%o.KTC(FNDO0v/H*sOmw%_${<nIoCKh%#%t++d4%K0CrbhYsLQkLVOy|?SZx*9<dq9]:ss3,X/3%5gTap}iVIOl`ZMB@n0i1O!t|]<
/:pXVLN2^?HI@doKlpXSb-OgU`5?QSs1glKml.s1bw@WqwG%A$[G)[zpzyFHWrCNb4Yv2^00Rg"VJXQOo<6L%n%XM!)D//BD-ULUr4UBD=y/Wsma&Fgk3/TeHQ(Kn1`5xBDwPmAb/W-sW<><>v[K.s;=]ORS0RMx-(3@=F/wdJG%qaN:dBLwW.hJ;3*i>"`ratP7y7J+"eq?q,cm.e*j>=feK6;WJw>[D:8%j8fRdibsnjtDIu;?Xb=5XhHtF.auXf9-lVkS;Cr^(NR9kN0s#^]>oE)WDwc?pbu;gE9*[D
JDJg"pbtdXmoHA)lBQZ~q7Hoj4Y2Y9*~].Cf#cW*vVL.t|2n:C3Qf`0G..neJ.iSE*-I(wVzZBh)bieoo5YI3?E0>071"v#O,L<7^8+M8`NB:UX8_H<rV:^FFz2UF1t~(.)W26_{WvWW1e2}/9*9qQrKud1bki2BBJLR)N8-)KMb2wH%b
W"D7.Xm~Sq<C_c%A`fbQ4E2t5EfJ7`;94G(Tl{.xDLAK&rew9PmvBLfif$)8-nj`03b}7$0}ii;t33bSU~JdM3`*SqW.
%##HD76NQ7p4h[yGC5h3/T0[nrlLMoc8*]0[L*~Mm"s"9)c4vy-sv4ps7bwi:t>bVtQjoV=TT_alol0]W`*Tf<h)UGMfB=@)}J>itwkadrGa+RkA+E5k(Ox1EjvN:e`K$X&3{/"0mfHBC3cbu1XTG84x?E!u]B
PKAbhDRQ<-kWulcFksPV9q+lPbKh"c1f;#HQDlN]:XPr=:];`?]];aCutr>_Whg"0UdS;fOCu!u044F/,^sr_0a;/?[9K4b)[!6wMH"z*2EGk(DFU:Vk?G@!^tXUCdw;&LB}pGjpFbmuV(k/DlyG0D1x)@PdsVtt3AM$S#RI,I@z20-eGOG]rPjS5twHZ
=s?!^_XNOTg
`q?b$Ar,vG0%w!Hzk1mfV+.v[dmOSi]1hK[|';case"pl":return'$]^@qbPDI(q4ktf,f/*T8I0ZJ%i$36@"x?fCWNSPhWqW$`h*[%?BnA?Z3pOoAyKYk5(LX^E]0k8sG)zf[p`R9y2G/vL
~S*kGNJ:Y<TXqX:]nH26mSaxim*xcXGV7yp4WV|Wnw*nA<AKlCRQw5Ml5abC-1V6PLnsOjrG*7*4XsC>O.L]q3)rP8#P8b-Nb+qW>oZK+8<$GOofDq_`7"DRbx3!?hU2|J2Gq
FK=F21YL}wO=,*F)21UWUq-IJBhGsF.ss.TMGFEKhUrFh]Jc,UE+>?8C}MylMD{@0gcklWfC?p^qTSFo%LfYG-*Bw-Ht
vC1
>j7+vry(56($s),ykxepusnX&dp7,mw(WYc.TNxja=aNSS!oyg,h0ytCfh=r^
b#5dK
t;c;g!t6XYj&_WrK!#`6JiZ8e#?}QQBJCK86d1lss
N$+a?ENP[@u4"xw#1EC#
+Akj~^7>pd}+#uav6J@J@J>1iXR_Dfda)?XMImm?ZKoYqC9EW[+?Iq(NQfInaS]/GxEekA>O*dGyYF@.pSZ/=sk!D()Q1m}3G=<:sfJTOQLH>3djG>scK.Yo&Hn?nMyq+3VI$2tqQjNN!JvW0SU<S8gP""#yLj;B=$w06jo6*&e;B1t^^.[?:#@0<^K"-B67+V{P~Y$@YSWXvo{5fk{fHMs7CHcSfjCFi?AI&e6p5wa2!"%:2iynFHFi_=}d@:D;(]fR_bjI|+~,`ZEf)0u]`6m^ee}O+*n
K
M;myQAnX[`?D7ZG`zT`T.^p8?
>:yM3AD=nb.EyEhQTXX+d
m2p:eG8AC#PAE
=hb=&Agu5"okCyH*$IO>qk5"a!=vYz$FZ^u^:3hmf!^S~u;f,/V8VM|
nvn2isDx_Df
1k.M4IO0ArHt[AvNQo,th72T.+grG(X9jww`lTD)A7d0(bq%:<3tW)l>!P)M5cS7[7.N^553LB]?FWRmawLRRfsfcu*,}jC/fbC&DL8fYBjTOJJjB
@TrU9R[>1G]hz:+rUI{%J0B+I]nPXAKy|e6g.Kzs?Dm2lntxS-r3/P%v+fLk
g(f56+dDARNDI|:~^%%_nu^aDuk61$JGG`
RE&p-,)V-@FQW*%s*md2!rLIEs2Fwsgy^Sfj
A+J]HUmnE2LT;kFSn_CGlN$)`|Y-q2g=WHx]c4MQx3_8u4%&be!+WTYJFfTL<2e.gn1Q+DAJphyO=%gd^eyiPkgBLMsWbil_*R,"*B0@-<%h9ui^ZmQKWDF18CReZ$q
1794>vfZf|d#t5(Ox`Jw_s]m<d6^Omln6XKos/<ftQ1^IC
n[*LEP1n/!K*Pa3LI)Y9=RPRyPCTOWwXKx`ZBZE-dM~
hc`&-B_#A>Gwvi(BZ?h_Og(5<J_xPp`",WmR)N`.!c,ba#/em+lck5-1xL[n{(h]rYD+]n;>po/LFD
Y+I#9k"..B>`)e<ytfa)^!:V$hQLOQU09rC[B;xA1Y<lKJdjs7%$5F!I^L,Bwv$&e+//*b^~grMs3/&1r<][(lezRM&2pNV(G!L)y1OI9W5?!:s``(L%p"&5G;+@M5ZLBrXM7roUK.-@#^g9Gr
R#TIWio
TrtUE`n#$MED=I_A"<8dVs_R":wpPfFI,#PpN"3Y(6Kk(@EB`Bb&t6miG+:jEu<A@Tcw]AwsGY)G=UXPZ(0Nn)T/@%*^.G}L[7eMVO!9Yd*,C!bm.U&J@mWx0l(lfTl#T2l<:B8-qn_-b$36mifF;-)ar9==p^v1p;[!1b#3m#r?Ay=0|Z[ALhqx`(Gdl8b
jW"qw7w&g$md:6.s|l@sBRyhe/a
pVbxTQ]DvA!a*v"<80U9GhEf@B2Cf$jQIQ|Pe-<Ao/)[OI~f=k-bwi[ln)HO>(}SBXds1:V?{VJ##S}$G0i6^LY>F<.$UPbZns>L|qF8t"gY8T_v#67gn@u*bNi(hPe5k=yp<`e"d
1L0&MLDKoc6.<2itn&G3@9?L$;:
HN~K_P"Dfl5:1;q6ca{L`Gy!1B7]~h?^wCr0e5dR-;vN0uMV@uMZ(7Y_C7;%L%5DuhW+-pC-s-RW_j3d`70?Z5
@2)sm`g^j$6:-$ml4`V%O!Zz"i9;whcu1)F5LF7kXhhP@VrS4l(5KpH4Q^$tunOA"kG6
"xGY`GC>9Y0Wr%ie`)/g$)59UL,*6Av1s7X*1ixC=vwna#+r+tdm1C}!4>x`<>WxIi}MCv>"3N%W3T>?zDSrzk/i(2Yxowss*x)*fdhIU5L=
N(x]8FX"JUPx58kdM
pg/+V5>x-!M9OdB[a.QcJbU:BOR/vP8s]J*.N`lW:GAEkis<?F#w+Y!k>MkT"a*bV{!SWys?v^jrx;1+R.-lwN*Lt0=frqRx6*HJBe)ZYH]gHz]Ch^1bwZ@Bw}Up+6W%1v9jq1s9n-_jePs;0w=q/B^t)m$g#Ia4/K_l(1Bi>@1c]
T7yrQADkbw-Hc-Ft<-utSR1=O;x)7}"r"(pKsUAIOc1|YNLJI.ny>[YD,p2;f9;qiROC%&R9jiSXWK9>G[f)alPZKofOep^/
smCrL8~N2j<@@F[X[+Rh?cqFqrff#`Ihxyu&oEDUhU:!O1$cq@OM[pcscAkKVc8K(
_>@sLRSv9*ZT^E_qgZ^aPB-c8&8iiAC:L#n"X(YJn7BX
e:"&6x4.H38jYh
n6K*YO]"$5919iA7foz7M5[c@(N%P";[xHxoM[MWc/[DF,Bcti?t).GGk]3^Oeh
kXooy
S?d5PY~-p:L,NI/i&/[I$F|S3q!H`>_Aq^m32J&NOdbdB8eq)
gS3)`kUeL:{c-*&D0
#fpcE<4"6,*hv+/4(Idr!aHnphLBY8wMs4KU.)"y,_,!lqC`hR=m>HzN1N0ZjkCY+>+
[gOVeRCV1VrZydZ[C
MXmT)aquq/PE@%*V7S{_f`-I7AX@sH5`9mz[EJKrJk"hrl4>ER-+%g_9@K$ub%!/FfS5NoMW)l}lr@A,W$@^Dv+(L1^Q#k;v-1a
jATGQ2c,`=@ZH$:x?fV@3U//AVY9#:v
KIBqjd:AWEX-Fa*-$KFIPM|S0O_pc#]RC/CW}5B(WFM5k-W.
60q7ADQ"Z_Q!Y3=FM&3A-pVbBJq7XBF=`yc-hu1*o6p_(6cLeWGRh1Y_#q(b+OrHfTBQ*MP`]bOB9dZy
l(F9B^E(W1+&mXVHjUv#Wv=O3f:xrfiKl^26]/7^:rI$4y;<}e#lL>sx-%MQsvj,7Oxnzlwq3,vxsqIA{HNJAWh"U`T6ng}ASCo><uhH<[Gup3]Lf#]V60*Da)GSir(xF[^tf&*TO,"Dt<.d71jQIT|LlCqnnmKO34Q[+uy]lY7E!QXeO9w1Vk&:C4Xl9%.^/r9
^UEJAy+Rwao(TB>nCU4%d
rBAZ&';case"pt":return')]^@iaI0}$#[Dv&A79G;<._<lR2=u9hN!Pk]Ir0w!Pan"6
E/u@:h8kaDOQ8P=ihdsXwmpvb3vxJj;PDhqfl*rI,TrMq"n^fjiW6<mTb)>Y*=]3./%sdrUr-c^3Kq_isT=qOQ="ms7}rpvS3(qN7(j]A
771ew`[kils;Uo/m.
T|#UE,>bC&fatEt)a*T[UJu5$Be;ed.hd:WKFRXedzh)qgF)T&6LcCOlo7)tPVS-03aui|3/J1t/+wrZ
m_[G0u)!Bde,XwXCaCDA;<ZO%DkH6M{8JNnIB*,U~*xq<Euxu9m8FVvD9;L6e])4"%}?D)_YY]6(tNq<
sQLztVq(isG@qNvo+$,Yj6/|gF7&"4`M1>#`p:k1iQA{>*1
2]&=CAdzlMXH!MBfI7Z:UNu.<`^S&s."43dH
WrCB7RNbQ3x"9JX(>49*y9|*_$Pos"UOVL#]Ttq2%m)3o7`.]ndEg7oZY_MK,+hMQM=G-
(b|Ao7r5c5kJ=7Y_%LE/<%O=4Y1mh)rPZD_M1a-,BB|
-P[k$aal+TfG?^
Zg:xeSELiw-qc=8ve5s&,g@Av-sEs5=i/[Mr,UO?L2opN;-Iy<2!/%9m2M0hu;2:1cPb;!:%CqHt^n;+a-_5v>x`xO`R^rD;d$B97O6fi-5)!d[BE.o<0J6Z;b9;ca&WFJO@O[
QFU<(qqxM<)<#*F+[_jozSGQAcoL;g2ADs52XFWq~B=[*BJ^>ue^Mis%2Bm+_o~LfRY:b4-go"u@NLZ!J!~[]oqp7m*fTGscpVqF8^q.XD1nZ7cnoymgHn2uF)+Q,;a_LtRK(3.5?LUvOH2UK2O[tKndKqhah0R$+b"=t?~Qsw>W1pisn=I,>dSDC#TCM!~xzTL9@!LccH?0}2z&AZF2|#RL+wjm&E/h2l:)4jwo&?f2$,:y7hoTBEf5YdJCrAO#kL+#N*/O+MNI{yAp8hs!,46e>ihvMYtRpG,coY4RaijSUURQ!?{8e<@"DB2Sl0B^fWUn^dJ,mZ~gzM,e"nxk-Sj$
yMLU*eGk7xMRT_TP3RO3T4XF,/]|n)tutMviVXD"VKyR<U.R&78]h>pU$!FQYe.3pJQ"gadQg~u)XlM:`4/}$OMUtaDZ<@Jdf|5|Wa5N8Zl9)NN%H8sYmg``-syJ
_/WQ&r9nte2tTX,@YgUkEuV3YiqM&Wm$440k#C.bqlU`Rd~anwU;OhWLOK|V;[jg
vAe?tT-R`rf%e;&PE{Qk-%4HNJAw$Dta9Ns^T26I]:o(f-]bGzvaj5Ija}nIJ(N-L)GLywZFJ=+BtN?12k>,,+EDi;3nSaeKy{-M:|:!6K[.y>
p/aY,*-rMaUO9h7*;7BX}RT,]CA(k[T*r%mXGIJqo/HEQoaMU_KgIp]ku$$9W<n.snj+_^VqY83N(09S{-?9Y>Xi.O]7nq/31bzY@/gHls>/CA"C+CCDoq.`l={lth%T6vJ6PbpNKEMr)^G%[17M&^Ry1jzeE>*2/d;iCej;Si&hMv}<:tv48[#dDrW,[kZ8X6KCZIT;ttk91DLS,Sj>BwA@0pLuuON8z*JTweXPB#K<v=xVwY,oj<bc?%zOdt+kMXUIrN,9~c-H(DH1X*],nX1"z]`5?&nmzdcE7BU,qXhxVbc9+q93!*GjZxmy[^>.1,pPS8yV=x!Eo6pDwaUk*JEZP
MMPMqJa.McKX.wDp`c]H%l493RJQz6vSf@V[#2TY}PY3,T+uh5)J[W|%n>%N729Y>RtJ:WVr@`a0>Fg!<p>0xc9``:S)<X+Q[@}U^JznjGr]!mG:a/ohw>pjqTK6j5yHW4P)ZkRc[,fOLVJ0XhrVV,Mw!Z$F}Gn"59a=KSUY?nG?Ij8YNK!3a)q;.lhtB_QI
SXjmG:IS/CSn63.?(/`A"m+jG+p@X_pY``tX&PN@EaaqVV4v
tA`CR428A5k9k2@)Fp(rZc;Gvq#rm)7a

Io;3rJDIMi*0~JQt-X"[;=&Xcs1AH-K?0!s1WY)_kYdS~%<TD)Lu9a!I:^sR5Wy`0r|B5P*3ngnH,oYLa`)/Xs,%`+se4,Rh5:_J|ZQF}y,LInc+3h}4d.F
=B$NG)6DJq=9Tv>ZR*E.4Dg8iCW-=b=r#NrWYdRl]ToxXE[jG1DE2pygVDD6R-xZBTJs~.517CRe/$e<0*r#BDd4G)>SOwkMy=->9.FP;`dHBHqj.&UcL[QJbB-SA4DG[RM?e,&=$,#a59{m.!/::mFL%i)(F2@?0.XIV3Fu}=rul5|c=GSi}TFL~QiR#"DRtAQya81iiF;y`ad]L2+`7cMG;!E/z>?O4wm_BaE>z@.f
C.dp:1(7tEF^T2:b&Tq~"YkysOS)Ce7p^S
W.R0j,jsz+8N{I
YCm#]C;s)m?N19,%#hUFBTN:Y]0vCGJk_Ocak)k2sN!*tEKbk~$S8bM]Ou2B2{=Y0Y1FYhQ71{`cK7U2jp4s5MUG4gdu@nT5oRk9Y[7/^r-_<%MVX:.%12$4$`j"#?-(#>Uf2T=V[/-K![R}Gg7RmRWJt5:BX?GzY%y]W2]H:?n/S!RW7>NmWWO3k-g4rbJI:gJ`Z:6Ql2=Fm{DQ/I`d9if3Y/>9kBOn)dl?_AM(bo8A(8xSN@GS0.;1X]-
KZ*<W(%-H_kGUu<A_rWih=ZbyP=ebJ3ujm/Pp>HkO2:UkiD0ESO@68O3U7L$r~A9:kGFI_Be9Y@?p2g~%s.Gvg@MQb0(WCSf"%Oh"5owj@v__)mEo}M3a(pK@YYeG|Ys&=YP7LB4IP#Hcn0~<nUZp?d>Ge)%[0TI%EO{l`
Vb$SuD}j#Bk-d3z[oWTCz8`8V_lKsdwt~rQ?)M_FW2IYaJ@K;Z4
HIU=(&T1uU1u1o.]#Rjaq2r7v[}3REv0o=KErrBO1@U!X0P#f.)LcJ%Xe@pn##BG*e`Z2>"_Bj5s-Y@YuM}(|Hsi:?PPlb1cf;@@G[{,|""';case"pt-br":return')]^@j5HmT/#`sv*AWELu;_RO_:9#F-dH3KY:_,fhF*z14?+Saz"My-GQw!ktqJ|Ggy3MaIL!zb<3kh8UhRJPJ2LWh?.yU=Z%N!<WM5,v|_YI!#Y-P/r3Ota!lQ-U)yFpPZLuBp1!0,.6D/GJ2qkxrM[Row.2y_8b)cu3MQ0Z[n#1S2rwd#edYml`x72s"A]k.Fo#Cvx=5j)1_"6H>A&nnR*?dma)-i&?gH8?
]cnJj<Av_(^N^*L&_}iDs&nK_X4p;_K$ebV&wx`lGsuqV*-rm:6PkgL!@[$3dl>(vY.eUqKomEP^l?(4NBX<c>$wIG`+HIo67GYq`"UKfN)y8|B!_7
=au:O`Wy$2NPDy@u1j9=B[p7ioC(<knn{1g_2Q
ZgPi[y(_Mv=z8$]bUa@?&V*Ni7U&*}dOcLu)eXa9a]_OdhPL5qJb=6!PQOZBNA]zTC($?H2@uVdF+VmNMyjGstIh9f&Oqt,B]Ki>miSv7ewJ!(rs>w.T;"bC5r;*AeKvj:YLxR**JRS4CeB)B&G!ia4DZ]-d.v/=N
_:?vO|=e32jkGo*8e1JKch6VuiX9eaw<W"(a/{DZ<i=%n;6ES]b-,!#A:3=CIkSRi*&,y."[p[_)R/3GQsaZ)^Pz)M=8^`Y=Np@A@K,GGzQgC,n6RzX::,0exUjW(TU~^gY#exvv55eZX{"u>}1_Q?K#06vQi$s|,DVc1vIQbdP`,[/N5QxDH-xx9XeMW$(yd~TsXWI`2"_!(PjO?_Di%WvZI??@(?UsifP[C$RurQs]]=%CBU(I<I7Xh"b#l{ZD1>PUa1m:n&6Y1QMA@%h+V9:yo/7/Lg>!GYZ&ynka3hj_[_(U%a/c`>#M4#T#6[,.IVfW#$k+A$+.
mLE6=]5V)9z`BDoB+^QrS?H6Im7ofeIMJceo_f>d(e?F^cWs.ngl*`b3%P
r5>stmM._iLz69TiiMU|]&6e82t^56o*9US7:8r:)f>v$KZ=/jF>v+=&[=($s|_iz!+2D,&=U9/FMiv-KKcdj&7R<UPCaHfT^LxL:jo,lGj0n_M9)7[$QVjB8g%kZA(Xb!1M,/P|%vlu:a%+!qFyNDs^7Mu8cK-0w>_F%:8[;cP[;UBZ<bpnjuMxZpK$H0WF?#gE$>x!%py;DH!?Oce=4Rp>/LV5Ng9
.1.}X};XN"b[p:n>u0U$tJu>t}e
HzcP?[1y3C%Ji(
Xd7J"=`v4rPXfoLopn;G=9;o&sAMxde"m*gO1Yz1gHTTsbk$B]SezBiW5h>h{=jX43No+LaNJ3eN]8?hlnE8/F@-s7UL-x,>q:W5V^Yyl+96m&).Qs;Qu]Cwc!c-]J|&?@RYr.U^u&]T6pWbR1#"
oM7(qa9V@f"3jj.>fy.D24*YNA`@%TAK3|s%V(*NZh:1`]Jei|u?g8<~&%9Y?Xs=O7kDy,2ARtM+Pw8{^|/MVPVLL7W:A~JC$UbH/xGwA?@:i9Yunt!Z&xOR^H.}lcK8w#6=uh#FS)
$&z[XPev!N4_~q)siM./kSCFx%nQQ=xrwxRjY2KbnRu?(VJlDX*ET*xf~9$-yYEIuSR=GMF"C-)gKxa@(Av:Ay(98e`mtQ(XzApuN$d8D/(CH*y_xW=?pnwg`78)`"|W6JidR5.%@R3mRTv*HcP8`IP0lLoa!#-=-W!U`s8w+ZO0@"#y=6Osqi.4!
,Xn]OY-Wl%5>kC0!Uoy;Pu(p[F%r=Sisog04pWxV28H5,94,E%@"g&mPsOvQusCihdm8T"TbdI.>|A&=SJpEzD}x^?7KFR054Gusyu[i6AWCO5ePdfMNQZsQ)?1T|rKpeuK0$Zuq;,.e;Fjb
TGVImMG+,Kk]xS"*V<$9)Eybg5-+kU/RKKKN,LDZ?KQMFd@cm184qzbe7ucq8BIE_BDd*|WA2S!AOEi%adFz!*0js]&Jb@AkwP7GE-BJb*nJ!;::(;1O*d^hE/_kado!m/s).*cdWTyp8*E*5>ujU.?,hBY7u$^(C<14)go-G>;)*Y+-rShg3cmZ6Uv1QZ
kau:+=E/%4&mG8A)HO3E=?(P>ovkP
V!J.l&5+P5P&^<2wzUIFYea?/r"l^
Y:31~X2Pi8`j8rbQ]R:3`t[CR"zkRR)^4nBi!0u1Llu#B$i*&G!xniJ,>1PQh0EW(Hap0^EE0GGfWLFIOUf?Ng<)p:mT;5^"v-Oh4?o<FjJva3{UObx4
NC`9k$1TnZ3]N8PqtDvG/&rA_x.}@zv-Hv]"L>a08vI*o`[>&+ktPdqE+7t"b!n1k>S6RRL_Md,jc;F"n&IY:P"*nPIgVn?SV.Z@YgE{ey6`6dLa(}.=)otgc|r2;!m5_&Qu0ZZ3(~=v"dbM>]kNL_l!jf<W4*X*(-[eeHH029CQP?Q=T.=E<aFSaKiUl4)b]Ms;nU!sGfDol$lrg?XM]QkD1lxawB+a8/1>dJ&@Ag`g@^B{?^(OkiiUW|kJ`k*!AH]`spgoQ>rV8x]S86PHhukc7jlZ=|X
ZL9NP2#bt6p$h[Pc<Ni3e4nC[iuu,r0dTw=6sPAMBFhjc,
AD%9&2g+q*QC+[?U<v{D#m^^nvPQFCKVOu}MDS`ToPtC_%1[@U%=.>C*(9rvoJ<NCGn@qDm5F8,koIHHf`I[f+c1(r.EFs&4Fh`0aRl;bA9TQEi=[@gEPaB8x><,.]ZfRiE=bMRDfyVJ")_:#O%J|.-n4ruf+T1Q]br3FKQINox0kw1/e_)]WM.;88v.LF
Fq&H;lg2NyVYys@|A;3QPqxQ_w7<3D&mj$>%$8<V;egw*k,yBH;l*M9[Y+HvV_$O1XH~mEc1hY8x10af=H-.YeX5X"UgPdg%S}*1heFV_aqR1J*1r+Oh+{@%e"S(l.OF3m-:3zU10`4*Qrw8.
`;e5-MZLh%T0`VMx<rs9N|GQ:SjD55
v_eT<gE
4anB^;>9S!D*"gC[89LXm2"^i%W2sncxd';case"sk":return'$]^@)h)Z+0&BqN:%z[9A4bVCp?GH00y@`UWal#1fv$~84#5>%+
";^QRcNvdX35n/;VPouF[lMk*;ZM[*A
F1ND78^JpJZ+w*RJ;^G%D~6Wn]j|Y%W>Xm&Q8er}95`<HB$7[N=*@{rz!XqFu`5N(u<+>on$LWyEKhpi;WObDO=
NMW-#H<d2jWqi]V1h)S=$)B{kAb%ju&wTUGuEx+B_(`M[OXn1dOkJRr.6PLm4&$LB6yy8U]S^I8rs
oWH^>Cir;!#AFh[c1aQ
f}su?cjDSGj
)WCT%,?s_aL08{e&yP%eZa%4@Hi?p5e<i^i^v*9b*FvL-giriXGuiic&;bI`
Qyr#bkI-xbwvpvt^BPf.8r(
VB%H()id,y=M74Lhsv>f=h/x:ln+~7aR-<V-h)h>h4Xur
hByOLi6F]#zO(vFbHd^AU:$o.pih{A--Q4t
2woH4oud:j1nhQ5b=7J,+Y]Uc>XK6]w&DBoI<ZAMrO&XoGDep,:^LXywRErn~hJVDTV^t6BqSn27SS(7*CC-C+9I|gETD_4shdT3#F`)6YM@EYee
17kXGsRhgQ1ge?W:k2."Q*`w--RF]8CyB{gGID">#Y>}Ij[WRSjfmJuE
7Ucp~mv[*YsNp%sZ)j<Wj:rJ{p5]*u}9(V&TmC#at&s4d:5E#nQVxYGvtrs-in&JHXoYfA-)Qa@iWea-!aZFfMua5ZHqoVITqiSE@e;mjrW.DipQ6e(u?EA$NqX&$/s6wQ1
KBT1y6gTUFZT<ZefD1Q(WNUDTZZ%f6![
M(b#:cMR.
;>wlg7Yf>A86t/9c#y:dACr12[FHBM?;sU
Pj8.@`ZY]]=7:IbVgZg_kc54_?a/SG$1My|B^1/$;XPpo"8.AN*nM009P/dTM-U,$IW:^I$^2`kXt@V[RVLV9Td1qGR?j?Wj?X]qiR[=jUom
>=[3p>K|
,.9$`Z-GkB/W6P~_64>e?q&G1;[+uF7fDF:KSX^@+Nfry_)Wh7k/Jg)4U=|Iws:R">-@vgB;<ca/[C*Xp4Wi}&3oe0h;;+prlf3b?8UxSQ9tto1251GJWB6u
!sM2mM=(A
+H8~,gy,%7+fwdX#[v89Og5yQa-SMhex5ot)q=*2FJ3!tKvKs-trvO<oA;^W8mZ@/GemVfYvejY?B%I"wF9y6v8hJ72Py:CL2QwWyF-^d"0]+cvh!7w>Sa.E%"d[Jjl-
7*k^8Vli>hebcA6oj8IOR<09PXL!g02dncTa=A8I[DPKVfi+0",?)i#^7`/o$z!yS+ss$F1L`"+h`;:_V"$rhtxs=GV-|@@k{"_TYg[uNFpY]6ka~Zv5j>udH"u0*LCr!lcU;Qwsn,URv%K2UgkTNi$c(`C+d&8GQUEB+U%*vX-$=:qbR38;jCc1+.Aq`aB(~6d=Qvk5,:OIwKE
u=:YOLYiy#|OX(BYo-[loS#@BW2W#j@1xPw"3[y:Q_AxxIV#8.~;7hWrYX6;vT6%"k0>_-;#YD7P^X$8mR*/m4Xfo)*6b"VI9ZA@~Tk8WN&Mtm1d9nX;&3.L#Lj#t09(D>Q(}!cuR4%Jh61(vw#b<FBSbfYyoLGIE!3,Cpr2UR/5SQK,~9r"2nNi?p~eQ)esB#gRj1)hVRIIb$PylW:v)9/"CUk5G8<T<8
TrOk(fQyYJ2rW.!=/k73.W7RQI)Lp{!SN(!tZ$,4-7jjE}9.I$prX&v%4t-$3l;#nXb"G8,T_|X>B`K=_w<V&o$"[y8SE#dQkY:M6fmf?*xn:V/Y`AQGG0B*.9!1qjqaN^#t]prrH%:f<>E7(OlB0#("O,GBX&H=/*Uq`hy@KlZl=uQ;!u3X$*9QE2nPvor3,B2
8[bEj!]Yh2OI#>d]oZAlW[(9:F*QN)R9_Yd;z%&I;]$h&.FkvRG9!>>y?j8aa8g2d^#aS(pYVbs%)NB0N&Ec?U=+)8t{e=ZATp^z1
`0>d0CB/7KS,8Qeg7L``C^cLY-Rp?%C@xe_bO."=>Cc/u!R@ERoeTovhXl4L8Bq+#_J.]8^eR6cQ;?K`F~WcHNAu[cfA2SEM`h".H)KHWwW[.0W.;9&*c^HB-kD?*}$A4HRS:&d%%KNcxJd*p.u[04DNahJm(4HMbRb~2W[nL7IQMU[3nX!8:|O([~T9P}RcK1mj/S(3fxmQr)A?N3EO[wQrRu,*1%)y=.Uhp>=C$^Dd%.tQP7*O@tsr@yTu6Ga"-.YC/|J`Epa%s!L4FO7,AOP|5_/c3G?q6i&Ag=@(5R>4@yL3R=mTXq[HTs<0IpL"1$9yw*BMg(O[Z4A^x3JnK>J#I(^DHxHd..O-lPElKVH|0FDh:#Z{l>9[]
_
HMI+o!w16~FQ76mi1,lQ+THxnZ>J;8E-9<@UPU^|3+cgv&GEgY7mM=f}3H],NbrdKks?^E&I4_YoQ=cZ</q:?=T`^+l//z3|j{NIt)xN2#:9R98hIX86VQdM]HMjXRcGw!+WuyF~dTZ`/*A6m}#)&403gQaa%&GJ-wO<O(cmo8>:
43-Or-myV]+W
9.kj150vLzn-]gFqA4rR8mgdApr24L9>"1RQ4BGp4ttLd&s_goI1M,.F_D(mloH5f.20sSWJ%&Z<soN
,B6#2ai2Zf0m97fQ0P%hpl3&q?`>lUbhBK<4LTY#RWra";>>]x7etC-<uok.3g;w1Y"(1GcxLaP(@SEGe0T%LCc5`qs(A7lJ"KsEmyg].un3>?cmxB;
nBfZEx"*jX2ln-M7L%5(cXa*NOp}$FSJy+#9XL,d[_qo;[/[VCs6mt)_-z$(0]wX+UEHw:B$HlKvt*.?^CvNvi=LV}DV6=lG$a[F8WdC,0eK`6D.Zh/oH=:=`)qb)qyYulhpKYL[vmF"tQ]UIqeM=)*aU6
k]J6R[8M]lWW_nl6{vfcr?!,A5L^3&z8xR6:pAhuGwkAyb8Y>!9D`)BURry>/J579y-VG1@8;
inYL,FbJgnZ^>B@tu*Licen/{[1v)Z]EXZW$Na^UWdB2bZO0Oy(Tg@|i#o:I(qa_W&)bxCp2Ky@)TQcjG/^t!&5Tq$.Uw&"OP8UJ1
eT0@jwK;zm7)q6^ZUEd$M``bf6
:^Il-Fa<e#,M+a[co_b1.`;^:ilu0i)D9m>M$|oQapf:Y)SI->!R7c`3E45$6:`/.dFs_3
[KL8;]_
D"pr|GKx:?,4I!c"s=|_,=J<|aV`J^A,
"Wpzbh76F</@^!h&f{5zAZ.iGn8g7J(5nIdYOT,7Ah5KS3=K.[=kVj5^QA%q&=Lv/dgnx4;PO[aS2KDkmV3pktd!D2ZamzgcLBj<?OeYH$ma`Nt",3`]TJe^O+v_G:A~Y3';case"sl":return'"]^;:h".!/#*#t^30=6s7A:!td]Qs.!$-#sx9!Wn6#.>vtP1}C@aEu?1-feujI^j/0=p}]NJho~83Ugg@Bymm?M]2cjIdM2h-`qM0w/xqBqA&K"%Iu$LUF0lo7_sq%NjA7x4!xln]h_FJoX_uE!^X6n"mjyyjOoJow
2+djWbn#
M]*XWWh:P4,EyxOO>%l52jgQyi,v>^`3|WH_|6Et46;WdXECP6gYa1<&qN;h*bXDWILpm`J/T5~$knz=Rxxi20VRY+nlsi[%=5H>q1;2!_(D>V+,F3]r!>07zC
O2F>wal[I
%MbL_=F.HU(vfiZPdNU4cPmPqCNR=6wmb,u_1@b*N8jUbP>5&AdgGpCnq*rVMtk7`{?xJH&(fp&$WJN)h@gKW"pfr39s$|w54)qQv~b5_
VwVhCaV`R`38^T`,=D),!`d-kz5MvQR:50:<
_UpcT/e<U!L)~BTz!DNj>`I`|q+u
vMBfEq02t;8=w:P[@D%+i[tS@ylr,PR`vHKU&1LmZ<lz(gAK/`",1h%B,UE8KxRtcMOEJrMJBnM<4WkzL`VtR0Iu2x7hqlu>,~t)jO#5oihM!an%*y$1v
f@FAK7c</S3@qO%9(qyllW^~?#tEQPoZN)lM]}_g9
tq!0OrowycLlA=@Fy[W^9Z:YP6?8gQwo&
"_ma8m(R1Rj~gI3cI34SG1]l]6G|l{`+&7PXh$CebLjwT/
eJO.?ZOQjLm"4,6<@4L-)A4tGu,ly+k$svIV*:U28rzy8dw!qc47O]MR>0*(TCbekog@|SrBn$#Xqi{<o2gJIXel%g62n3Ih9#-H7iQ,0`gKaaWU%MA8
J9?PP[ah,N(iPfxWpvWd=WN2MX&Y
-$T>7"NimUY37GZgzm8C(*8_D[b6V9jZn6w>oRZF<qt;L@<JJ]$2J)!7+.L[RX.=#cVdJ&jvK)o!hVORYTN,EMw-{:rc0a~pgY=;}$~.:-+Y*:q@r@|o.yA?@16igKt4j]2sU_=F2k~0J/xmOE<
{n@QM7d&hn}h@*E)1<NXtcqUw#
1VH!4{(*N,q/y*#l0wctManub?yjBAD;d!2BBnvqXf1-DD$Oq"T#N3e3TF+h7`SzkFw$k=S<8DP(&w)*pVmbXRcdTtx;o4OR3nR[hj=8H-
nXAjiMi0<JXns@|`,n@VAe+tOqr9_-cLFACN:OA2[
Y2Bt,Ifry4ow^&QhwW2#%ykN7^gC3lEy+.-7#%=+<a@82yA>bkSTcnUMUp56N$D$k#Ap8I>$_ug4pg0Q(&.QOT;SjpgG^oz/r.x!4$$N<)mxQ0n]sQMm-r)JZ@f"L$1%7HejJi3.>DCq7lMe
Aod}iIL^A(,i!u!N-Q]sEuY{K>Q=([gT9zk@#MZJtvqvqO3CSMguN--cmG7$:C:{1fPXS>kjc
IyI(+5b;,4sF7hye`EU*0}rp6ur_,7o4K)
bs3)0?75>00+[O[7!5|`nf$t$QN.jSZ4[i10:3>-e_Zl*v}ncc/+nq6[j1/(SC[79m!12,<:b8$j;?iHtG8jKAma+:Vew+Q?4z)9LFdw#k0(aq*hhNS,>AZ;jms7eeOa6YtQ:i/:0gK]u8/;(e[y&80.^:k.@4VXnT$M<fNH3MS$oHVqWHM!09#<d8ek~bD1Au8Ed?l-vV4nW.KT5WYWV(+Tn<F(c))GYA42!=wT2!?n<YjDpydmQH^eS+ME/R8>uq>:hcnN+"F*>)3hh@YM*UUi"?:m!odpnyeOpC1.SqkXLht@e!c#mC/=~[QJ}n4nZaR`1<7A@gO:CEMFC4+8ASraBJQI;7Tli!+FPge(;Fd<h

f72^@5*ODGK,TcsNQ?+@]GkS5WKRDejI
IcNK:/kx:WD%Lbh[%)$F]xo1{nd2p2m84Pb
Bs"(wSDeU*uUY;$/0NXwD2/mc@k-9L=1[+
SU/}[ZR$8$fX)J]xH<F]8o=(^;iUl<`M
M!bo)8L)k().w06cHo*I>X
g.6]dA?YB_4}y"F}R-L|4VI4:y)xGJ#q*8&.)&S1KchU$s[1huwCF=FodC*IKz6DVeE^WFD=iJ2]qdV#>liI
^F7J8D9)Tl<vX4rfAA1*JZuLUB/"8(|WT_OfLi@YU[,2b#C3X2S]t/^]FvM)L09V(8kZW`p/}$?xV
QlaSydq/SPH]ciaA8959d^~1Llm+=[hov,),9d
L4T3B=:0e7S:<K0ooZ]FhEdB0eQFk$Ek47Yk]J<bZ!!8F_P"2Uay%eI&<v%kGbY~TAd$eD=<;-bow.4h#$5`D{l=
p@?&q25sF(GXj$rqCe`]|W|A@&dx:*MuZV6PV?#A)70X)7-8pV_rfsQal[#3}GSt3!5xLrem;x_e"<VAlrbY`R#w(hCy|@7g,r}tOS|cdlLkt>%_X9%A,H+sD2YA}D^jnVDghNc-q@^A1r7D^(Zo9XWxFEcDjGNc.]]o6%QXvb+VJJMPyN*Ln/u=xoU69,usSC/CaS69R)V.:ezVXF=rH#YlrQe(N9==Ew5s~Ze0Bdy,$4-)<B77&BGpnj%*s:d0|3A+J/9t>P{uy".DH2&,oY*ktgJ+G!-m0_zm~d<1<ta!/Gvk{Boq)k[`Oo#]4ai!|j@m/=;T%TKfPXKV]jo""@[C^:Aq]JCs0l-nq9ScP6pM[Pq)&/uI1GFB6RwM=cQS{;unUfC<N.X;6LfL]):W~`&R~JV,iL|Hd9*5w(L?nG(@B5J`-O.,CPutmNvA!iadwOksinZo)=p[Jq-lO)77niZtW$kUuGguk8<Xw:-Wr#[<%p7IAYbK3o?Y:(PNd>dm|U?=NcUJq#;?NZUTn^<m3ouk"tAn`=|-r_8PHL#x/Kq4&53#.M?C]XHK})0a`0@V~yf]W.6Vf"nB*s}65W@ig;GKo?STFjZRk-d7=]<&#TvFJj=></zAD-wG1fS5
6]xKmM(l9
l59cTQ/c]wCo%GDrgHe;]=.S0^gg+"Tyh)Zb_Y]j]faaB]vv?[]9oAt+#}Mv7v5J4)<Rr-?F;5*?HDJ-x4getJcOF:S]W
()&8g,Z4N/s8f.f3m1F{",ZfAg@PVUc%,8beI|4B([mA3ZgNMH.#hOTmu(.V;]Ed#GZ|l!tX';case"fi":return'&]^<&6KWB$e+p[ycA]b^XwK#Z%LW<Z
2(B6(0(?8vWv.^DX(hXk[ybR:)BxhF(KUQ8/8ua*WE@_PjMR43Cbon/QtlIx<LR;3mUBImb9V,&Ms2Ars"ERU[fUMBGn%-@Pkh4+=*!MN5l[830A]aX,ne%$-t:JGp@_nrk&ICm5f#`v1kn^w@`4A/y~AWaYw
J5yEc|lxez2Mc[?iA@va)No2fFoO<0i6mW?1)-#xAOf=$$fD2JF)_)VZ)5^|507q&k""<=HMec2PZpiO+h=GnI)qA3&As+NIeK,gI?EsJbN|p[i2Cvofkp.8U]SD(cple#l:&leC*co$8_yjE]ge`c9M4O!PLTGqO}
2%h,K982R2Yl
Dkcsoc4$!EH
geC!,b9oWy7^f
D]l]199gxzX?:~U/9&ukR@tQ"s.07"::<n;*ly"3n(UPx=44:sh#^zm:i[[j4;5J$P85^!K<[!Z84oEImER+L><F"&9@[M2AN(<vQJ`0iKTH+@83"kWg*KBBhBF6%MF{"jI)9K5UD%gGQoAp@:U=P@*phxs?xR=n]#xz(ja[E^x**y_g6q(<2e&1^HHnt1Rd)Z+NprJ@seG:&;X41QL|"-C>B%0%Q2oK`y:!*ci&%w%E8WC];pRJ$bS3HC03$M<AIRr*AhPi$0w5SF<m<#trF@8PlCZ&NW,h=>RYB@3Ad@9nBH
,ZK;XN^&$)H4Ud/qGb.F)x"4Ff4+Yqd0;#Tv^J0$afrj&CAs5qtnmW>&3$]wBha#f0ZaM-g<lqLR"Z{xNI-!Pl:vh&Q3aR@nDIkLtIDbeP?eM9dDJe}Xk!^hKK^Rq=(UByfljLc,z54)jZ7eJ(raB"*G5%zi3e+=Ht
7FA#Cn0KaRdKC3K&_Ll8danlXd[Z[bF1.$SZo@)i/J+~%3ygSSZ/o4nO%XQ<-*S3]b%rr]p#6((=C<=@+ZsnPu*zs-`"wDX%AMSC.10$pFyE!rN[8
ZT#^:AV,UcBNLi+mQzfuE+k!)qv~HalP/iw+dB+xqir2qc]$VaB56xETeEV&[WuOb_V)ZvoZS=y:g
PH[<sHHC&LJl!bCLI/&U%;Em[55@bv*Jw25cKYgxboI<%aF]+=r6&tKzJ`@+A@fN"sq^n,mV$0*#fQWf^O^KGS"MXG*]#C`C<Bl`O;DfOVHvgQ$)C)PT_Zz!wG@kHJ86in
--2&s@4%>C{M&(&x~-nyayz1.]
u|a+b-()f7:8^BD7Sg/)Jz-$j&(=AE9j"{@MytVvyttx_d^<l
bb={9rT(t+L_YOg!f0H
`3;jK$3<8+tkulM.EZp)5dKdaz/w^]Vo/(x!DGtXtXv*]tC5v"Cy*~`sLA@T*pW}[T-?@,aXpZL|Y8afD+4PcIo=PG+~o@(UYIL"N+DR^Q22eYFqkLykB|[-g}hxDW_5>Ovd[-F^Z+W<2?f}OrsE")n$T[GqWg.efaxamuZooxYd2EXRZkF:5.0|-mBy-E;9X4^8p+V1E7Ik+HbdRc)FM{oQiFl.pXrCLvN2v8l,KvN4>dQ0q!%F/.5{9hG
qjI&P]GB_<xy5muK5zC+8K>[$<dYhJ@SVJQ/4/?I@Z"]G(X9`h$ur=qu`D,(]NNi`iwq]*cr.@8Zj<J+e:#v7u3]#@cVrB6*hrUf^dQ!-v1`h;R{"uFA$3datZKH139!1HOC=YMl*j0qt>e%5^G3
S1Cru7bV/k3mp]jw1[ud
!/k~H#/S#==fxOneo+GF-ZWQ;y(rIT1sS~0:AT<Ia>^73]Pz/TdtawUY)y?keLqLbyXu_Ih,0FlkrD$F;]T5_C1zv.+MM97NM(V5gb>PKIu&]+d5<zG1My-xR@LUB<!-N_:.$IOno>YU0
7^C34c2x=TYDE=8}i#o7V(X<>v3-u//wT=c-Bnddl;1qexJe_S>EF",W27/F>H*y*7lVz#0W9Qf{i`E9Fp$M0!<!"b6B6BDz3f%zdLiNx{yjKQ/$Pn#u7N.6>Tj=,@6Lf-T0EpA#t456YPyiO4eNI-
"c^9N+aNv$
@Zp]snv`1-F?p)y)`"JiPl`I1s39tGu?q#R[`KCgf<2H:A4at"0B<Gl-<@Q
RHa`XSu6%iWBi]0.j,mUK.[Q8J(n:
q+Opn3=%g$/?)>,YbwYA35]@m[4/S+#ag?E"t9RHD!<DAr2c20N1E@Y,`<JgH)$+ZP6Ac]C!A|q;J6"LZp#2qNdrBX@s.?(t_Ud~c`L!n&i2ipZ`AO(Nf/NC6rkyIh
@nt@+R8)39}"h0i)<fQ#W"aZmAj^P(
=-"u1y^;!PhE!O;hc_[pE"2P4))ABm@
D]G:KsavXk%ece(/[we$+_vR?ekuH%f_qG*=P1Kp^
lIek
<?3%jDEQO2gMZ6!!6omeJv_mVrp:)f(i<!_CeMfhZkyc8@M:*sH&Lq>$2Yk0+5[5s=LmYYSaq;[`189x^-J4upm-?l"dZ.a1P:7&E$B46R&`kWhx#D#t)h88wrkk)y{ZOZ
cOB/aUy#a0WUZ~,Ees$2P,_vhly#GHqm$BL?hmoWADB}g3NtN"-b3<cG+Zq/%,OuXN*.vTL9)[%P;kd<_";{vcUUg[aBAT.>uyo90$ccE_x<B:R3IUMhxb>!J$>bwb$R%/iG:/t<uZ0(i,%Qu)Y~F^@!l2X+s,lyYUGcyE;8htuQufWOV+]8c_Y3u`B?eH-~@aPH76)1H;TB`$_VbMjGDdrCK]//)R.DK(?q]Kkb/0Oj,}O,0,#2$6YVl=Ran:I;b(y6>P/S#?9m*(#ZkAk68TM4:<2C/?6(`aF;C/O(;!)0UWh6nE4yOL8r1~r5!uOeuXVr0uCRjMx&+3/I9;#+tAF|Zk:iJ/;)NEYh5IX*=8+`S"/oPr1x;_qxGXoMML<H<76>>BbQ]ylcMTTF[{C}aCy5Xe*})lfD6&v%#*78`l<(qHW8[
hobfq>UP2e>`/&"w(<tMZ"0~1[/=U_Q4X.n<jln{K3K-C9:$l?[?NISbTuD_&Z>+2#F~l@+V#R])h4/6JQ(&@il%kH,5-,!pO1@FRSUP(KPP5.?";9J5%GFt2B';case"sv":return'(Zu;:g~Z+$"5$t^)pVs;7AKjH:Ng;g3Am.uEg0,&O[N=SY
EM7X/r5K2&(#NWLrw8h@"#;WmndKTvC"jYctk)7bw{]BQ*^0vqh`).&wb
E#,#qo&qD2od73G<DOyB(r?He?G_G;n-u27<n$SN,2!Ek;:zpq-cFsc}D@&JL"*l+l)Cp:Wq/?`r?]g@pe,9?zK@hlW~=.oY-x8/_~@gPCvaB8sEw`GlXuAeMxS|
)jhB{/kTa7dWLcD:&
iU4$7T1]b[}mplj?;[FAkgSxR?akIt@7n<+:0_hGR/n@04`biR?d_W~7%H!Y-l1xwfZp%6Et>"W3HwWWT%);NKT$ynA)m2KfNj._IL,4_nVyQ5Z6$bvJtXTc9A<6"4?WJpdp!Ll$l^cgO1$
m7uhYD^"ul6s$c!?oH97|86lTh[>h@*2mQ
`[q*NSP^vq]m.<Rw)IwIhmJ.h51F.2xu.j_.
8pd>@82TpytQ/pc1ZwsS&6llD;?@2hzL(qrVo]o@*s{2h9es=%k:Dlgp!WlkNj-1><L%zK8MciplR?NPH.1DxHB*7-dJ{nR@y=iDH6?W~[d%x]-F}9aUs-~@A4Gd~XCn~<m`&p98
D{<]A46eW?g(;gKR>>mP/~9Te`c)`Rl(7lV2C1HDlED9y2rmY
)Mq$#RG$J=H5lrmlF^?&s=mIU3iUCG$Sp9V1%gl9K=>R]j*tf,4uKuLj@"f4t9
^*85VTY4@bQ".SFq!C
1vF~DA5qQa7W6y$NL.rE`?EE1)E#`<wUku":jdJ.$}6E7|?6p&nNSq3hMaUrEO4M3q2ylN?Rq(StA"l_9R2AhxWfH<#"K!cNw*l[Gyxb`LTi<.m!2KLPS.ONDg+/s+p;`zO@C=RLB<K>aA>[U"Sknp?*oi&HCjU"M!QV!UL7!<r_%&cpNOx`>`-^X,*CU2TJ0X:MvmTK(AtxY-fKFb:mof,rp^BbycUHC&eRsWC$@btSMyMN@G+QB
K19aJ`)P;N"qCW;hZhcD5;#OCU9ww82Gv+>%?LtM[`s4mUk:%61opo^])b*c/V*~XPi&=p#KUJ!0"6&x>zUkWV,#TaDA#4Kp
(8$LtbHP0(Qb<xOw>nT?cy
k=d#9?P^)X^+A(d!L@7zswLm:1>JhMpU/E)qd(HM-FR2Vrb(9FPAr!hF7T9utxU+PvXXeKRpXewIXoRs=nhLO,U<oKr1=MdCCvD(6bD?LMA[Xs57Ml386~80gxs[W2sG).y+3gF5g4gz?FK8
!HtLa!0,].g19CK4p@j!~6}C)X9F,R$]/EaDA;VBe.;G#C|;#1j?/opOIi=Bb4h>#d6:*#,tOp&&:(}N8=f6ns%aeNc-v&0O/ixFlW2<A8HS#kob.riZw@t-Ih)bc5AA_Yku{I0(rI9FW+YYV@6/^nJ9{dyGwxFa?,EfXrx[":[7Cvx?(Ow902:36A+8=uXI7lYl!.uo|#+_~cC[nVXUjBh&FphNQjmN`(CheI(!:gsewEm+(rXp!0js.u>QGe3^
3B4;KTo)UBG
n!L&2m-sB7BJlVI7gm>NGZHq^-Y=O6T}hbw};rv<cjK:ta)6ynupBaiTv}BQa5v=_PxyP]&:s6=OTB,D:M7i3$#Ct!"k`)pPN2gqCQvSLQu8-Cu+@GC4RCL@
g(
mxOvvw5gg+SURkyLB*LrXi5$6qan
/U|bn3{ry
)_ut:(i
lTw.yXsWGXbLorXk^vcAceHNCCG(ep"g;Gzw&>qUZ:p`yx9ft<BL_I@X-,*y~CP)/TI&=2VF~p+-jv4Wk$k`d
/P@fG?[=xU8n4ATQ~pLhv;x02F:sRUNnAu^T~#D>(";VTHZ.VA;cmx;GbE!L^/|RilCHZ-jN,%fTq+/q2.-++W&Lz#8`r5),33`ljK>[<?-L|KX6v4hy.L5,8[4$zD
9x<G5H1EU2JdlE3>*(IzAg-n!yjS+`%0jCcX0b$]6lu"u>FPvNZ
*a<<g7q{Q/_qG<D4hU${K-A|3W*{WQ:Ybna}&tN>^b0Df!3*21fK2vELjI?bdWZPK<W`XYP
97+DP"@PQhq[Xv_8vwoVSU#J66F7p2UHe3X~GO#
!JoVxK^PXuZ~y1K&TTNX.dMGkM!*"6$I.*+MOkF_Rya/*!pQ5;K46S
xQXYsG^d+n>a&Xjr@+u)hpzvfB;./Q)Qrx_*3S6*-Kt_t4vyz!GRxu_B=,Ct2SE*s%|$7OVJ|1A.*,{rBN-Ck7:R8dvo#"*#RVQRps@>E/w>550aIxkCnRPN*$oKMjk!=wR>P$&1HABd/lYt0Ad>c)_MzF%!fY.!|obO=]>f
*Tg+/.P_.CVdD%s,sWn5wdr
[Jy(a,P8.;#HQj<NVVqR08W&RR`k/Ttr=qyosYx!#jQiMwgy8Gd),N0Yaq0>VVT#rvApfM.lMiSOws<,5`9:6{kN
[2gub4bodGf8grelTXzalIdK._K1GTOXIpM<{@1=i
HMQ"vx.
q>@4*0as^^)fN*~As[<PipYI}EVQ`D%ZpSWo-"V8uk30]p+8B?q+}=^xx
gPH-[FxUD`:pn>0ta*#H{bu!b;d$.0O3M]{A)uNAe&s<S)u.YZ
ESON(-5]H(uS./<}d?#<j@RA:y#kQHOYN7sLN;
|ZdpR%}.bT!MWo@C_
K5W4Jv]RP9NW2I7WbYdwghtvx>td2Yn%EBJ4jg3Y(xu!-it]n;rYwXaU0r&yNHo.n4]@PL*!ITy98a/MiB3#qt6p
?c-"=M8~J80RQ!j/3+_Ga@iS7mW(
PyXD
Ol8l6S,,gW5Q53L(A;Y=P5q,+7&H[jY|u=uY:dl-Um&5yL&~H->g?-[]X/MC';case"vi":return',]^@y]@Z[G.t/v)lX;%&Qa|f.-5HZgCdTltLQLJ:Dg>]nRh_jfz0o%KE<.J!arr9ZHvAn/&;v"NU^Q~TwqvxKcBw:A3`@N>Xz3AFc?Dn]ZIM@tBcnZe4#quKom8V"J;&9rS`7VesB)0l-oC_uF}gZ7rj,xbA8U6vYVUarQI/}nkR-#e@#ePs!6-]FeJm#Nb5,/~[*+)!!/6CK2>$Wp%DdUZ0Zxya1l7])A>q(X4Q4`E[P$GrFQmy5mwE&H+U|2{5JKp6@=r`7ux[
X.5ax:mHR-@5gGWMZy50`u=S)&wPkbsB(s,<V7f,rzS2oyX>W=:)
%jEYf,Qlv<f6I>9dyD[5eW3aCeWx!8;+G=bXE
{;#0jJ~,$QFDWnQHirK^/it/Sf+Ue27$~YD^Q4GyMBqI&DRoIZY09U|2>xTd$Z-f+V]>3]}M3vA$onxmX2N/0ia/n]XS=s
b)>WSMmM0@7i0V6YVZJzx30m7R<rw3H7:+tDo5`<1p9US~p3/WP1sD+xSrl2KOZU2gZiRg5zDUWYxa=G$5JoIEuuMH`~BPEJ6d:FGbOCi
WEj)Da]Nm3LibYIwHaO`axOn,xuUw0]!0!w%5xe@GGw9^6geyX1+R7#eR!=^_N:gDp;cv#$8,{(2L|[^(OakoCXtmpVsgYM9sa]*MPLP[^,.2V/r?eMk1N.yFF1UXTOQkx9jGtM
_7%x[q6}S?WsHeM|e51JtdB"XHGj3*_g/6oN[vmhhzj,7UM=Btv[M]a3*Y.}>b7`p+2<!WoFk!`%b)fB]U`[=HaPJF("+x7{=5mr:7jHy|aNG=9.
2Rx4p:3b6B~$y={s@;NM3AKI|vJXU9aLYapr<m5fC#yaIol8gg7AyP$fmC,;)nLxdY6R19E)xXSAqosm?RnG;-/tn(_gvC)l"!Rp@OWFSLeGd]Jfv:g-9#Z3`Vn/ek!e*^X2{$Yp<b/$uO=VndcIl*Ai}1@Vl@h_l1Zqj0}L
yA!48!iyqw2UPeW=%abqXG1`odawwSswBUNS>q<B!+N&T<!K`x`5!:JC!XKS/Nf@Njp~;MZ>U!fM?H[KTDLvV^FrM"*"P]qokBsdqyeNYfD"#P7!2es>Lg>}bi(6R:(k>my2BtjE2794y}cb=
;}#lx0&ks8CF$__?oe%
wR3?.n&v^3Xc?+gXf|Ix*Nv+.rc}%FU"T*;+UbY7fbix.((~.+kQ/g
5$p8pYq5,89u-+uD
Xc!BIW6f#Mw,x).Fr*9"mXM}%`P0S!pk_BWGDhPz<})Uenibgmgf<QLkqGvT%uhW"qpy$W?WtWggE}W`>NXiETFp<S_&NL6"Z}!YRnLRMfti]M%^%a!PxO,_Q/86>pi%#OhvB~"ZK<[0S<i]SBQ&%b#p)]wP%/s$&"=EDL2Vg)`c7U/Tb
&9-u7jlVwVuCxann"jV%L^^-.7_aLIf./|Bb>)v:XjDgGmEBmPBM%eDeZk>R%Cw5ZUQjXL*zCF*q;Kn/SFbd)7R
=kt::&n5IA2[al-nx>(PN@G~+]LU#yW7(dj}MxBsodItYX0[4Ff.tU4#J~pE$KJ*:q:Z=A/UNlH
%4]~W,kn&uAWoe+UgV<~VXIu5}m>!eeE*^i.;BoMdv*VoJui1(N*Z&>pO^bg1%<(E>s0QVvW)+TF$q^E0Hu{v=TeE,Fbi_Wq9gJTx7^b!7!e.bj&7ca3>"b^4
)UXHfsY[?]2tM_s4%sPEe*0qvE1pw*1bc(FNL2tj<qcwvzA_,2Ax2N<^%}h-y|2|e#%sE*I"48wsxEHY#KSKs?QOKs)
;9P^!YFG`)$~&cQJ)i>5x5S?,v;,,nNuCR+Af/GGX%#-sHRSGb37Nx,
,M?GIE:ma_gb$Q6yF&G*&S=bLvL88e2#6*X[6KqUotT{Y8mhexL0&)0f(:).7pYr=IZ
F_K7.=okDm;s3@Cqio
}%2jH,L+fGwWRYDbEcw>ssf3;D*tD[Nr2*;[M*SSVs^-50lZ-lt:r&N2?3,B4I%4yk#6eHhF..Y0
>JKE7yV19Ar?QTAeGQ!h8X98_e%cR="P&CePxal"f
BbxxwlT?Jl<7UK@2:bV6;HO}-m2%FHeif9Q}@}yw8i@h(w<}Rz><E/tqT6Y29wrgDyr">c"V?Z(QgVNG*BbS/kg,t=-{-D*1dw[IUNB+snQ_6h6xtTk>:7YU`EsyZBZ!DF86B=c-2jR;Xh?nU3<uo$01oUyl*Dwjl2m@+G9Q
hP9)Hjmv/`!&xrnNzjx2_HpxQbZ2j7IV=/6Zg)@N(C$>8=e`aN`[hd:v<J`oh=BGSHT0`y.Gd&(;@R=3Tq93i$ncf<R"7VC1*KPbgU@h=<^/a4Mxg6i1x8t##jBX3CY2s2n1^618px(r5kR)-#z?]8qBdi1&:9FB1U03&*VXZ.rOJvjZRn|"h6&84XKgs4%+%HO;%RWCn/|j@B,fvj+l`s&SH>(kk8,ng;]f8jQXP<BrgCkDCD/Y*m@f9FR>p/7@1YjN([Vn{1m2/
V_s]`(o1IatxY(TJOIR<Xa:;("Z9y[FYBEUuRi1(JJ
YJ]hlex(
n@@V/^FZdQ/"yHK/oRN8B-[=4!,P7_2gUfe(HF$lij87Pu}fz_s!C]M$Sgc?+WK:y[LY8_M(z_VtAD*dN+UXl(LfRy
mNLPMCmI`T8c,zsa*Zk]dI+TZak4LOgYHD(2QeQQAL%&MJa+@3"=Vu_nT1XAFnS.ZP?QApEW>6%{,Fd<0mBv3yXsJ+7r[ISm"5cZ"eV"b)
jWL4_X+KcIUq7qZST<@G.[HFOH-uY]OhNn=h>pBO6h7E&wAId/D5YG>>NSp>L+"p_V]),>9Z3LMJPu{JPWn66dwK4w@FC@on/]Z
53Ki[)o5h_(lbKoV5U.[KVws*Enkmd7<8"/qe#vOECFs.lZu8CBeq]Xl1W/*-;RH#gfr|6E4ldqu?mF+3cTla0P!nK{,;G*l*O4(a_e0b;hyzbc`VaNS!eSVc/$Fc:MW`iK#A@Q*G6o/&Ng77eE,-.682?J5m4bX~q9lJKJSO5d`.hOuIT~iFkO9rcxBt&V2ptsIV&xM,98"NFb[>96q(nQpG([0mqnOhAQDMFEL&Ra(u6_X=rebGE;4|vpMe_h![<fU&d~MRE)vLbM!{ZtcY#ol<o#(BLr/}wu!NG0/><?,m%vA,d:bdP?KdygC%';case"tr":return'.]^@aaMDY(nXc={uao,2RNVMAEq!,1PC&`0"!-!i]y:?"/5X9!gb$ba"AlKw+0gfOQEspx/tPbr`RC6ZJ!C@&gJ9_]~Qqj:ssG5.PGIpWnf=omPb{:%"%y<sfj7kMoFS$bZEy@K!5sON#jxGf.:Y@v:vcg;e%KK$uWYExkY(|Cbr@
qmNH2j9oMK&tO+`JAh5
9WK>$sp80RwGE<&Yu^eIedbsGdoQ<v7Jn@yGi6PiJ!ZIfuDDQ1*UQ.-X-yfc`@ng(-dg(20r,k}R.3Xd1HNAj+5kT9S#!e}M~@$$m7BBc^$DvXlWKZw+@1%&<<y2nCzLF!Lxpc<FsG(x6px<(7Dk,v*+%pBw$]+HrqGn_x[?K
5PDf5I
ygR,RMAr("bh#[uPif)`+4wWX(Ot2fmy=V<KLC:]m`[gg9GY6
2M3%`]mK"rO]e3K=M
lXI1]CSn$a"T&DZ^smyU1mbv,mYm[+uv@rAu2QLCmj4G
-sf)b1;/%Xz"p$+AxeDGVN"u`rrq-(x@I8pE~KLm|"*p0Ftl8:rk$.#%-.7AHeMS)rlg
c`>qP]7F&Z)bpZ%hhl+nme4HWGeY]#QCx}<=:TD
8vID4}$2TaH&-LNX9vAYqV<e#qm>cc+HqvV$gLe;L=#1s>RFb}fkAW;<kDM"k16YnLYZWU-L5}?cQ+9n/&q[Em0u+ZHOEqQADC-N,!l!PIpwQYS#wm*u(
]Ek3*Fq0[wN/YjZY_J)kwXHbt&-T)#r[@xYtx]n+IAoVWC)^69Co8U.=b=ShKG^h3V+zXRgu5+awd*/:EATMjXobV%4)F
&kMp8}&$*YfJqnv)s+:Rd[o9i<`qmx*|q>(>$mj#f`U6uqL;dRfYfuOG,gdjz(x|3L:EKL!Jf]
)Xjaq,)j>2o.Dp=CZTD:v!fN&2g!BXk=~GRM~jESKbHyv+k-?uuG0e];&upBAb~MxnmMk"<<2^Oj&Ro.ZHlU&f_U3A6_IuVUa.zy6IGZZ]}bzoeY=1Pw/
rl/RM<*[[4#Tn0ja|f#&.1(U~u09:]K5!.(2zq=w1GJH~/zk=l,g#<&%(m3HH@4
O[$<eO~NR]0p@X#b6Ky3$o158cbM1=.CIq&o!(LcFpVYU*8?|MBo;BB=vpOBn^wqC8g@,bPM{CL8%*C4Ma4;HVOwbbMY3J
0UO3)R_K
C^ZnEr?nDvoTn.y`]cu(e0gDH#
:4Z)$wS~suEL
}x$U5[DFyeP1S+TMm5kEDw>R)OnZIHF+w0<XUssa*yik"lIZ2F]/cq"@ki=S,u25wOrdM?t
v2ro{L;=O&5jtT(^
O*[`dq78H
uW$y$qN@MZBxRXc[D0I}cn:Lhsn+p_h5f4E=Ub*JMY@?vbp^2E(GX5*S<!Yn#E`|DQq$gU5
pyX*Zf_lg8<|1U<Yv)M~osd-d);&C{-H7DR;F$O3(?Nf=L,%Vj*_ZQs!Llsb)fOBP:UbfZfF:b&qSJ]}*Ie4ceDj-/4:R<-fr*-*k^P+Pnu1ge:jmL66fYQt%@F!%J:,ON=/#]uZNq
Vf<A1hNHk!$jC!BC2[oZrK
u,d_#}$;Ty-sP..[U4p=l#ym[%$!X?G*8,@m*5=-WWs>@hedg.V9J~mE:EIY:{FVs]X,sZk,v:31#J8DV^m`Tw$w8AdA-Q`Cu@y!R$i;kEd0Xu)|NTX5czLjwuX5al]T+m74:~Oi3Lcw3VAJ?EyKpd%~H7jqSl-sYs>z>g<eP5"8rMD;-0?9m;0Yg"k=Xz)doLp,
u-,N48f4Q,-od3
a9&|kw3inNQiq~x/,]jYfiHhS.*6UCr8U#mF>RS{u7tl=u7xmYiY$*o8;NKW
8Q998:VwKF:?)p&5|=.?%ec7"5?k7P`tQ#PTWc#R3WQNi;*eXNvT?qEo9S[^b*Fh3*Yh5B`4+W!eTxFEwO^V9B-?-frRfPIxDr&VgD|2:Si*Byg=qZ@S%N[G)]Q[8V)t~pXau6V<-+<QkI@oz1w52qED<`,;Pk[IAne@k&H@
]<T5DrYI!y4U20z(QZxgRSD4;%c(2GR)2l5(XVr(&vI_H7iU6NU_>sP$yjKRp@a"-3G)6PfE0$Lf4#o
I$vBiS+b0%_iBMC4D0;5H.]2s.=qN|8SSlDzgTB>yQ@u5u^{WmRth/O0#<,u45Fy`nuue}F$%|<A3$bAqEGg,|/V%cA:<9P*S/I/,8XnbNlu6`"`DDQYdAvMYS/+(9>bY$m0O
5#Jb.GHt;6da0{pUjMrI0!"R
%Rs6#:,4N]"9EY:7(]MP(J16Zh&C}",l*HF#TaE=dl~!7^=TP(ogJYoaAdM!N2e0/F+s(`%X=eq2TZ)57Ub%Pa!]f0:pt]ETt7QR>-swC^U1,C$
GJP,Z3,<)-5@;q5A`AfjE)>:8f8@Y"6qvX5GY)?k$U1H>T9WyvV0Qek8*7q^|:oIVf|-t]=X;3$)tLcIY%Nt=:Aqp@5qD!Be%v8wGhWRT+DhTuZQ]Z<K772<Q3FmS91Q76/h&0e[FPr0t%-y.;n2U2|B(VtG[e%fpX?-On
r]@0Z3!xQf9{)d0x^[CGo+X$k/ZyC]p+>"7PBCZrEqEFBbfoJ@i:jY0tKMd_5vc>IRl?4]0q#T[3vh8}<F`7*9(bk<);/CGu4Y(3]4o
&ot!*-A`&T,:!^S<bdk"G"3_pHGDj2:M`*6`ii-+gv<R.7PQ_;(?Qo0J$3Zlh17#Q~j?U{$ryRo*@"F`DxOw3=F2tGvCQSc@X`a@:XDf!h-Fg;CbrCbsw^/t_/
7k~QDh4n_^Hr}Qq)1A:dM@$9jp]^MfGO}8!CpSi#5plffPS.^
_Ruko?LW{n
!agwm,S`9UG.//B9[G@~-euFyEZkvy$v1{^h]9k-.*Jk"*TPwjMG
tF2>K+S9:W$k7*HB>q5`z=g%m
7kADY]1h+TRnaABrzbc+rOHEfAz?,B9=tW#$wl7h^HIpH>6%0<)PQ7Us;LlvHQ[A</_Jdy+kr9)ApfL$|/(W"YCm]2y;HJ(t_r_.@b(`?_0]vg#qPPobHN,DLAWqHYQ"AP]qDWzl_ngk=kPs|)Z$sR<*cU&+"[|4nPn]#Q{LYf-iXISO2xayt,vp7x$v#"lbmJ>@>LH/$G_?AcNYAlyb:W.gwI|bld9%sU6m"N&';case"bg":return'&ev@qg~Z+.B`oo7A51t:c#@@A@/;|"$<{Up[y>cZoXO+X(:g5@-#X!YZR0j"Q)$k
sx7Q2U(=m+k(se:^v<Y#cr=PbGAMklB
1n`q[(k{b?*7]?n<<oG#j7y-y-]jH5G<`pA^jK49H]rsc{x1%c:j95x;X!LI5s
Qb_dpWv,94uo#4bB<F7z(yWc6ZMhj^!H=d8?TM{P?bW>dqqyVsah~@LlBXT_/G%g#YW8f#$>G#
-&W"(P$%,f5cMgu7r!w.q/X?_,x#/&P9%$d1vhDeATwuhjoFh;ow%D3^;`?oUYP{$O-jn9vqiO_=r$VUD`+%YgA^c#_wW3b#sgjzE__J5_C#Ew*o6m!a30Hook5>+fG5u)fyR)l%P2W(=Z,Sl5OX-RJ&d>ORH%sK_wtNs+@$VQ%*OIQ[Ht677b3*!m%0imb}pgb!2oO+;O^~.vAGQy!GUN7(__DgP%G_F27Ekj3$.znR9Fo~+i6GF!f1VF-q7C2CLYM,7#U[N_Nw9m83qX*uAE$G=^v4FjE@YEdfAF.4P|Ka^6f<UV4-wTkdc}$s>2FB%9y5_G]sD5J[=WCg6-0xnx"T6{6(,S!;&khyQ8k]X{_=R@dPoOe&mhWd7P&7A%dY`jtLObBr/3QUf851QzkWw!/$
>uuZjt.&vEGrskRfqA(i]
@dL^nu:0$"SbR_nAVhho5jC-+p?HTIiwVXJ^b9zQp9Im$=?wR$6R}_!`u>5DmuSpFZ9Ys[Gn+-H3b]kQ35{<>7R#/dw/6K
<|8SV>1*iP>x"CqWQ?YyRU:2*oys8!CZh8lUUg$@c@%V/.#q8XhYbG43gy5D#0Z[:h.Po]A0YG(hu6wrMOnWc5<2FM#M5DfTXDi9[!1&k|g4Q]I-*MHpQ[!+[=Ufnj7=#AE.4p13]N8v&qb2Ei5}.Y(,Ybg7"*+rsXK#UjW)iR8@Yf#&t*!Rj0
I8Yp]CjqhWWs@+>SLt+8{Z[jfHB!OU#C#+
BOE$#*_#*83H7)*[+XkiDF-d]CdHE<(-T"Uy!D1(n&lnI9,FG+GJgRWC]]Dy.:im0bcRQWeMc#DEQF.geqmcfCpd*j`bfS.cls(5(RO
l|INT2U/!cCd[$JnVw-esXJ4(qRd(jt9B1IE=EB}p)e(`T/#=XsHixxW(|>F99dnAme~2e$?L]9lLCUX
Bp>V7po$LL9M*`LbmEws?&W5uRMX|ZdT{VfW|h+a`9NLK8U$rRsSa(]!f$OCc9my1dX-X>4!_Zg7Fq0=5@/<nK]Kuis-0B|3Q796F8|?|fQHWP11J/tbj@P.P%yg6f-EQW`90Z,2[x#=X_.W*k;SQi:>k0"J;$L?$F"f#&%jT&1kA5WNuj@kt+a]9ab=}r[Y^/|3M;VY}BRl-!%d[Y$Hy2r+*qnZ/=ljgn5%8VzqX;C%/
ljQclwoK<waQgycfXC9t`JP.i>p_>tD#9ZFCr]#*Etv;3`kx
!uCi<].UZ.<_!VOx47J=eWB1yt7/_@%_+<vzaoG-CVI@8qA|cny~L>T,[[/jjc[}UbY]iWXi/j/(0*KHDj[X9Px9/z?sIM&oX1L3$tjYJOvx+K=2e7OY&29u,R@)
kj.YxaP6HmT@Jab!#=^T`VrV=B:#p:"%1,X(NfffTj=2Txkt^wd
d"/X>k*h
B,x4=Q^#]ajC>{+.`5G9YDd
2@UJY^K}w]w>yKiZ<G^dxCU#?0>H?~R(W~W4jb2kg]jC/?6$*4UN:oYUAfOYP&-5QNFFx6(@m[#{`sZMIbo`]
Si0<Q:@dT
Sc+f/@%PI~9ncM9X_GB?Ar>A?r54gY,}Zy9gLAD}Ba>nxuM(L67"m/Y{&tZ21u#=@">1vF^|Ztqx2::{0;WhunZdhqHgbom~KLXzZZ(+_Sx):HrL^_CL!bZKrRVZ*g?26nxbUJ@r7!41Q"VNfpNZTWuA;x<[#erd#=0M^2DH+z`Wezr#v&F
?N4U!C#t9KMl."#<1CM
r!.L
Z,"?9>H;k`1]3LJfq6U4]?i/:50hEgb?H%zb"]^e+Q$V`h<.AuYkYui7{b&1zQ:TI
,`$wG>q`20SL_P@wbG87{-n&Y3}JT1b$I"u$Y-xPQMnaA(rj$,kHBL|4:v>bp[zP0hgv=Fk@4<zLL+Y0.yD+^0y1Gyi%>W5B|*qH-8udD::w~"n"fc?D5y4P%j<M.QWkGEeepS|W=@~g%-X7O"4tkb.&wuoUYB,WT168dgwb,D:2ikX*bTj]uB$CDl(B^,LG}RvU$:=pVHn*o^T:DwRWio:Mh8(P+KxlRp=1H85)SSy2*5>6k7};b7<#JRUJogMcW^a"S8XNn,#6-ZlU
/SYkc!W`!g/.A,CLPWo/9OaK
2F%Q/pfo"pp(fW+H%$hg`f:i5Nx:S;7*TP|N3.c$/q}!4^UBR]qfpfS]MM?6!SwN`5Z3{i5iY`/=$.s[R.CcU6i%C<Y@`3-c9pe-nt]k@<LJI%sjmO
dM.E1tR%f15Eda3KCE:-5p41,hFggay0F!15:5_l1qnrebbSD5BJIk2AA7NwMEC"<D@X3~6+hm%;
/"A&))jO46v"fK698xo?ir%QpgC8Fo~H<`hV-3?@r74M0emR|=52TJk8C4ku{&;.:r$klmBwN"pJ_yLrRrk?T!B0dPZ>RMsBHc/?Vp~Du+5P=d{S3t)$9poavy@Hyi&5=^/)s)~+dSpxqv;E$v<JN3wWJvi1{Yz`},,?7s^%gfsq&afJ2J1+NCMtgu)OwX4`,P~qW>k)u
S65qADCT`!@G,17V|fm-^Tx,I0WQ/?E"Y4A$3yHt".@TE,.^doC+Nq;;+8ixm-vY}Ny"JqUn~a{V=6]<KW+qU%TME-MU0.xe4^}f07hTAr7*89/bsCg@.%d>-h(j{DCfX>tTQqn%g+ZoqmL[JAr[@ZGTBO8.U_+_,:KWl(%[Snk;Tm?uk4%DHXrh)
R$pE7RZ4OUjd/-JYKtlt7<txTSXy2r#irw[:eOcWTh|
#j:4gD~R32j+&Q{M3f:gInRU3Fo+7HY017OfR15*)R>.lCFf>cDL|DB*lq.hfn;Nv(8-EHR_ENF?z1H0
U&YBAf);2+,Y7ZOqQf`,e/ttOZ8UVLVS=+hbhHFYG!6Mdigq!:buomvN`+d)y9dtf0mq*]SsB3%(N`>{AXir3bgu/5x<u5&Ttj"(G$3lp|1l<hSPY$(#,gIBo&NPFr7h.drVcvmwFAq0W&@-NJrr69+X)#O2eZb,nj-s!Dld0KJv;]OX5(KYIr$vd`&?PmVl,?b-#@95z%8M6gz&qiA<,l+c?2VPZVWz)flL^I_io;(s>(DeFya+
L:6,R;TJ>0rg:X4*P0ggcp>HH$n@XLf<0p?[_0@U5[IlFtc4D5y/*Oi2|xa*A8>U?"-Nj@1kp8;ux8A&4oeh2W(97u"EOeEg/Z}/%;4?dJtSgHZ?4AV4Ke*S1nRa1y`N.<%M}(k1ul"K}=_`//*MbPWvYJ}74:%gpZb(rZ9ciwJQy0_r{$d;Jr5MjauwL8c.C6;K1guKl:Y7K):(Ol&<xB&RA&CBo!,bbY5gX1{R_NS&VDfH+vRFK=<6B<vQH3R(.j#`YJ@!V!9M9PhafAd)%cGf^?XRJlT,gK?t%AkeMCF:gdNsC2B>ig1rH[!Y6i>rJ^d6`,nA^.)+@C20I/(c}SqCkKT4IU_.DTqv7TIsNWAJ-8}u%/9
V4Q):I]vB.%#%i:0]>uF4i#D>#w-^9HPTA[6ltJs=:!SO6)c+5}F"uZv_xa2,_mZc+s,W
/N6';case"el":return',h_;:aLZ[/$5$rXH@#Hh7qt@mWy*{[.XPkoQ2.I(4vnU_)j_<3AV*o!=Ug<TpT3]6]8FE(9pVhM@O3LI(+-Ho?+KnI*cJeV;Ka$wwtfhNx0lSu`>&6DEG7T4|y<D"Jl;n0_;Sn7hX5z`8qAObc&TOj+`yH<[al0Y[^<>3WMniHJe#LQO%W"c|H/52SBP{/11%^5R|kihXI,a3t|rkW3=yY[09V{GjxP:8M
`nl#Wl(o:Bp~E+H`$bD[!tU2CL?QRs<]vZ$wh<9{)OGF
G[a*)9j-vb%x=yZl}+^X{QkL}:!YC!nn:ESS
I2+0#tPqs~e(Dq1B8pW%qxwQc=KuO4E,t>)JVETV-mA3`,TWPn@v0B-=CQ8QdN32wE3I!I3)I{dNR`32Vn7kI5R?5Gd-/nWD8WD?=O0oXrT[;w&)g?eXI`mC<crBQ{!}<C<;sND8;6dnB{foLBXNgS-v3yI<<lo}8/5%>da;!FTx`,n*)BhC_4+VDIh{&(hL$5(ki*g4dPHyYZPo:]CJOFMUa,QL7X"QbV5U$xW9lFu|<I!aAq+C59SX0aS"Pta]_G<Sm>>uEivOwc+bjjC!YQd$ppJx_m(xeEBw3[EAwr>!q/F]2GBm*.<_*x3*3}vOTxU0nmeMg%aS+wsHaJk_bzprIg^d0F:c^S+f0*eO[c<}Vq]|^CLyq<OI/*>qmT!RKJdwP?`d5mq>/Q7xb/rBj
7+SyjF_H;<xW%8ra#*LZi4I&S1^G.bc<
]->d;Rh4oGZ><yX+D3<>cc].!Ojuv<EJ-+aB@h
Q#KnX1NL_7fr!noe,rP&eIVOG__g9^Myn^,*m&G^dfO(C2<.M[t9+,CIKi#ZX]j4O}`(C@TP(zfrYq[nFF`XRM(#aE`9t=Pv:_02nBhO/f8bvK@dyAN`^TXC12>T3M<i)nr92vWy"_l?ib!*c1IZo8eXSALlWbKOedX]_0N>b&G@X<nspTJ+%x6@@5yFiSLRB^qm]^5*6@c`!fEJ033#!wFjQKy)O.&8DZu!%b"*Jc_^UH(#ysKG:=@HKq,p
/Li>Q/&lVwFL3An%zJ*vdK`M<=3C>3jE_.u55FM+kyXuK^wGvRj6tC^YvVJx&^/M;T9`|T:CRvYO3p<O(G*%PiY^ocr3_Jl"2I{@"
VR@ViY!mryjtGp;9+!:fm-5pm;$Xvf5pLarWykM"nhu8hY,.toL,*6euY.rL_HcHS
84IbQ`(;QqWseh_B~t[%6Szk^CyHXhxi5u<e,UiIjL47S/kH{k7ZP+$TSI^1rF_y6!c"2TX*HV{_q&Dy+cZA2v@y&gW*jbg@AoHYF%+MCA4E+*HO]>K%lPo#[+OA
FoOFh|4C
N;`p:(R
C$FkR`6%]-CNxm$r2!=vP0p&NNB6PdGMogoh?${`w1s="[0Ukl9=mjVvqvJBUkW.f7wP/WJPx#G*3kP^>D&&MF>u^3>!q*[_{*.MQ)V)DE]P3YtiAI!C
MeN&jF>;.zS%2.#o0e2.ZbbNR;YV8_r`u]j}Y)Fp@f)E0xN6!Q$Soy=aI}88<r1,>O0~"%@gaS_y!G#FO9=wS=HvpyL%@i&B?5MGRjyh[`4~%:aGW:e^`e*0L.S[ejsx*9fI)csIyXl$f)/NCQE]8X`t*ho%+sbg6U-6I!a7N*r7iYGp!_"^2~6.D|oalc"[B7`hf1%{4]/6=aF[M%yN^v8cv[xw!.dZse,P1,cF%YY(J,QV1cPc0<n_w<nUZRR(tz>>l;mfUv?I_sdOZ"Z7#jPxNDiR`b+0oL!Mc/I?"HCz1#MpO:vH:cf-oOUEt^;+)4<57o0^],f/xip~Db^z%*Fg>c,`J6]KEx)}n:S)))oGPRJsy,4HrCu!.ZQ?9q44WGJ5JU)nV;$C0<ca(pZ;yYj:rS/l$i`?FO]P4IfehiY4&y,";Z_cNR2c)dx(/>=u@sVbE?po8SItDb/hUrI(pG;8`D9@
&-t!,:qZwRWp>b4XG+C"p>{rWWpj%0Rb}6(:wEAm;mc98Y;U[(hayK~?%m[3V&/R{6%F8Rhx@$3x{Q+LYBe"R;:tuEOcs%xgBdOsf)bh=]BP&gm,GQ2XcWN<`Fq"a`hwb3Dxm7h=LZnJ_/ldsSf&h;%QZ]6_kMcM[O?I$JF*<BN^^PoU,]o[0#$-bbqB08MSe>FO;qY0Y4KD~^y@>M
^3e"BSDWZHusc[exJ*GfYs$+-D^[vpSwQUlGQh2MwS<6n2Sd>ZB,ts2UJ`C$=6W{=S8:B:BZtx:.YaBs1)$,Q]^:Ys,>37R]pCb~9uJ2sBjYWVnhl;Vp#omZ#X/@Y6NzrOW>o+^vZu]H?^FnR`-7l#Fc<8e5&v9Yp;[z[Y^,1BW?SW,w4,^gQ<r+vuqEi]yfjig
!"s#(5l?]#fg;<l;f&1,wdPb6_Wyu*k|7Na1P7PND|<7[{"4(0E]`?4VwBU82:>yfxhWFs3rS>]*&0mquLTos$VUXid$D0[;Q%0z!OLu5vR;D/uqgBR/L7E%x8P
aPsdfx/:bxg,VH5n1FE.`**6Y$9!iew11gk>/7FhU2u}m;dPY/._f4^hNqD7T0ctc8DK],?EJ5=(lh`*kr!B8pF*AM]ngYUpi
h`=*<qv,jS&|!%ZU0Qn8Jr0l3
#t^>3GBBq^e%;R>3KkvH)Asm88``NN+b3..;t,aQv
"bb}@`A1&NJbNU=M74WKEc)RTe:8?GL0!r!B<RtGD
3(>2dbbTrHKkX^Jb3+etdZh,=84[0`(yn+lP&YxpB36Y<6]]o4SW(^8Gu{7T
1VP)8l#T#c2;C+M=[iOTFN-9PaS;K[-,U51m701PaG[t$cbLM5Kam0who/S4PhM7?*9$-C$?Fug6/Hyu|a+;:wx3=hC#YD4&^eh8}7F6Jgw`"-SG.S7`494lO[}D#iJ)dK7(x+oUL,dhs4$b+pPaiD.`$d.B0Ca?_:uc#4mZ<
6]="[[;rO#8uf`qeUk7%?QK!zgXP)Fg]lB5IhHSy{i:uauT^&[wHgfu!m
s/ET93f(#JbCEpnmjgfQ
,C88w0-N,tcZ@m+8D?8+0"c_q9J;8PcF[&!=K!sZ5~E1
G4IO402:03@1LWz5k?o@^"q@|HOIyFzm?&s[km~k*g>sH>M=m7di8j|2}ma@1vdKbRzv~%M-l*&#})LSa"FRq=j`PIkdk)%2XwsDB2y>+kLT>.e^,+K.{MGQ<i9t
vu#c.%a1;8oJcCs/1=6Ri~Q"*&6F+{Z"<D:kb"AqGKEb[i9lBElAUCh.%?9%4n5bi!-b%AKp5$DKx><~p7O!&]m!V_q`w@H3y}ny<w1xl&6scJ;pVJMRlV,lWJnUiPL}A-!V6;Ote1vdgp=*F~]!G$Ott|@>]]Hy`w6/F3%IjXR(ZBUf-qy<Grj6J.tw$<!^241+=!1H<Vh:lNxq)tqzlKtm2$kB-=qRAd@oS,(,:b_utFfG(.3s@g9>=Z@qRE7G8#4Noj)8sQx;qjl{ql*k/PtF(uq;hcHh!pHx
L<v9&g1sl9$$E,iMLkCh.XC_S`zfIL$w1b5nSpsAh+4Tz]lctP_4o`vpHaE_OAImKyeIsT5TJ&IQ0%/&|hMX5s!m8s!JQ`E1=X,Er_COtCqmDpN@pt;6q;|x<LR4aCwO9wDcXVyc"TDhCWZkZXCo+vbP;>uZ`Z7l1a!2;oqJX+ViE51luMtD6cKQMj%LlHTlKk5mKi19r?=CJI/]#e*?k[69ihqeL+^)A4&65n(_ds:&%Nyg:KAEky
1O$%N?$=pSla#(`w:]/y;F"m&lvm$5N9<>^X4Glti`uW3C
49f&nv$0CR#b$47l(We^=w<Te/H5m*Xc)"M?Z_hKq:<P:KQj63#^@[&xy(iYxw9I`J(]t#s;JICn-]d0kd9eKl8mUyw:pc(HJJgc;xkP-`L_Fqc`AFDJ9PWc|b(A>=3h0TTh>HG"`IOa:_gqk&D&=%+IOoOf}uYA2fDj{;?Tkb&Sr]Ls|rPBE7~&/H%I-FFb,BA.&a:dD81nbH,3ya"s2rF])."^K5;JwL36DV9vI;eg:lw5en[5"MO^42$B191XJb`!u%{,q])2+$5rxgxfE>qvQnUV|.XmHmnmQlbCb,Q[vGCS&3)(gbgTpt/!ZZps(JA]EZ|Jr-jW|&-q_sT^;A>M,3:uco{r6k"
$t=;rEuvA99
d*qV]h:O?r}]*!oO!K8ebg;xanmdKG$:Tqr9zfOd:TNx)o)';case"ru":return'!evATaLp=A[GXlQMe6Q/jtDhd.PB`[97i+
"7(]AMepFY4B1;k/e{X8[}9R1t%XK5_YXt(Ml="qjxHEXC6UIIHV#M-l
KbFgJz%qTXj1d3d:CWB>iD6lu(3FfB54L
tmt6vUX4Q`CFZGctmEO:e3gCwyRxi$U=9x|%
r3VuDt-|0%RKR-YN1eKHwa7X1hWSectDW6;l`@t#4qK"j0,|Rqq(8<k0hy4ta%[43P+rxzZ#,fV}V&i$NNIK:t3R4ppOG?PLMOw`ch3_t9;n<};ll-_(+KMgh3)]`fl3k^9EtW)NmEEEK]/lWS-[=s>zO<S%UDc3B517h0l0H^5OJSa_B_gW;lf,:k/`Ri+k0@etMr`hJ&$&F"muCr."hSqlY/h;W..X[CTh/KFEg-U$=AgPZv
h%L
:5yt#p|=^5[#J*t3f10B/O<+8SYf<A(8.I)e*iSZ,%t94CM*[Gl&[7aD`VSiAR3>L%H#kebka*j@:3c:Wd,>T6_=fW(p?*{xOJS],NyEpfW41Qe1]eWIviTi$_ev3/3@s"<kd1
_n6jav6*uwJVpr@>duaz==xQP|z#PC?<&})xc7fekSq.esn#W!_0$N-/8:Ya0q8rs6;itaO
>Uh09(3%k%
j&!+(2M[~G!:g@~l16L1A?@jkx{oO!qQ0a3q5/UdFrku;8alT71Em@n4e@yUhAA@&?t`XR;7I$Cs%b[>2ym,;2
o!jp5teJ0:KrNb*s[E3S.&9v:*9{5a#"LEQQ=o<zNDDr7L5DqmeW?K`0th@ZKojDZtXn4iRRB/m.hE$##!S.7Ej(b%t3#v9d!39+s_J`lyfHv#6K&Itxw_
Im4`wEE;MhNnLttxFkpZSx[;
96ZkdygHv4+7ABWf%@_MP&!AS^Z$9.!1)qV`o62(FjX}y6#b6IjfpG"{FfGM9}-4X
>@I#u&F09qEK%$=ZsA+$j!r^V}OflCUo"i^hR,[CnC!XeKYynioL1xps:w4v//pb)k0
8yo2Mrm*7M6t@]!eY:CqGY28Mhx;`SCiVCxv]8&kwbL&nUxfh>-/-uHycl8bWO%_:AL9"
<tJhw"[8C^!5yg,&Zf8CX{I|`fO(kbA%tsVFmIx:=1+BWlQY[,6U,8.!=+(gtc(5O80KD<!9^}d:2d6t6DMOPCoHr8XWgu5tGR/u6tp7lJ2!mX7$alYkut:2QDQA9jbqt&VGrG6+lVGk,s"&ac28@PK$5Z8MU~GIB48$"(gXl:bv4BN.)EaW9;("j{wbo/5:QC`JHpE(7}tys=7EUnl<`Z)w6sIPRC#H-4vf)yf^QDhhLFLd@A<O<
IA>-#GTdR8LuD429l
`-(aiX0<,&).Z-5&UAg]gzd!-Nb&EeC
2e1^U3N;9}&[#~I}F!kND?K3/o2K_2sS>x.XrmTMx%>@E;aQZb#v0+^:s0yW`bh_kE]IO0yS<iu^3qj]Bzqe
$l>I)nL?AYEGoD)ZsyP[cbWi=/x(@.-=%(@TF*y&et#3awriW@dpTC65/s*=BU|Pajvi!A0Q#8JP&c5L]t%0tlhF{q4KQ4!lUBer^PuQ/A(H:(Qqmes6&>B1WYD]KgO+#cD95W?yH`*I2vGN:]21b,qcdQ82qKmS@5rnm[a[y+;$<+z&/YsVPF:6h0hyWQvc^;Rdat
cQKKucBU/5Y7?vf{N&UxC>phkvl@$1"CkDBVG)%Vqg:})0T+);1>k?eze91{];TQ:"QZ`no(`fGSgTS_msmf]$::C;?0Z";LY&Bd_wj5[6K
D"_QGCZNuQNlO}Zuil#0cr?iv+/Ik`QW&V9
Rn$6`oO^A?ulhVD=y3D7rZJ]>]*XQq=bxTP5j1cqbBL)iw3-i$b4y2DjVO`+1b$./Zo-s_=
g&JOwRo:"3o{sIeN.=HD=>/
Bd9!6WTU#R9F)*50]c#u"[qcv+^"(ofK59onD6W}9~_h1hx?R#+BFs(+uY#lu!NLM)In*|S#]s2$Qgf@)z;i"(!%q%vLU/$:_`%sTTU4VR/|lO@c5vUK<LSbutMjvb`+R=6F,d*],#rS_#<PLTRttmm7<p`h@*db"<)I2S0U,S,fdfo=_mB]]cqMQqR$jdr+px=zWqBbah2GWss8"ZZUFq)1l>Y4l50f:}Nl%vvg0
jRd_NGTr-L@J=9U`#X;:*;!
0%Hn7aHViaSVYRGng@/hH2Cl2cg[;r^;7MBSPHy[v,9&(Hs|jIe5<UnBqx8!^R)]rnU_7+I>/j
ztt8?XQ=5;>VvkIq+0!)kkr1L_;6hwK_Of%%xQaPGFw/X4M?P<@V|q@lQ?}
gBDL@D0c[yuu|^Y9DsWQPZL:*EMc-]gFRW2qv3_Vq%Q?;ipP}E.DyelQA`rv4Ux3aacSsJ^!H&1@,^XH%#EL#/Qk(8$vv0c175lN`[J^NrsIXNqY]p1BW5De-r"><A5:o?<A9p8lNa0-E@/&c^=:M1U$JkR,mgi:3B{G]^8K3iWJW:Ty@Y*^sGT(P93jP2u*%9^B$Euv5k)k3F%_!j.>{nU+R;4*wWC8im~94
4M<"l@#O&2!T]R7qy.*yNjIBiy--|VHWoXoF|J7/K]fW76qWma]
:lAMRT;/!.h[jum7?]l+VJW:R9pJadSF;cUp#d
;1?=hf!"E&`^o3Dj7pk[cf8wId&5*e:>vdk!d$]wCBNxkug|D%h]ByMgy/B]4K2,iokJEb@/OdJM,S!N
BEfsN@VPciOXBszU{r~
lJ-HJnMtR.apn1LmsMO0/=1CV+Zm{/R8[s}L&i,83DD$RAcr+hY<
-+e,#S.":Z
"yrf6BiC(ZEKk!_.QAF/O<NqM6lX(T*]Ot3LA$:J/c82:3UHq(~I8SQhhrUZ~;@,uJld"a*vk2#)Ddg9:4fVQ)Lgmvp*t_v=F0Bc?@D-+KQ!S9~j,"t<I<1[%D^eHmBDPeA3v"#>YGV@D>@MFGLFy>7=dk]<`h)ixsdZCd:b`q;4<a!V(IneqG%w;(:T$3W_tPWX-RL<wD}LHLxZxAFCOCgEIK+(DZ+;[A:Z-5}&9B*,5d4ng<2Y577YlfC"eUhZAc9tfc>75+FR{(lTyepf:I_1_#6VC+K60`2/E/G^
mQj-^3au+q$MgAD"vtOnkX+7$HxG=%vtt-)Jshv0uj+@5Nqg?"=^q81~^^S3e<v)c+*l!7/;?;RDKS-Sj4Dd-[,$4`BE*GDuUtb3)jFt!w0w
,oZPRQJ$nsP)4dLy6r@WLCzFi!F<HtD>4b}RldzKoq*,x!]xJ$6*w6:_K*O@l]:0Hl^b32&E7C4<?+37y#<E^rv_:(50!/U2JXlH,.JF2_iA1"4e|Xs,tR,t#_iYFv%TPZ2GouQbF[8x]);lM4%?3k9^nWX:N?_$yAC<`2#TTK"DXUOPIBo77l{[E$X3yN}s}j+dsZ;lvP%wfL,i*bFpPt_X7$67V7ge>wG81ieaB;dUZ#j
!9JqA9JcNAh#&nvW
I~c7(UX
OG4JPUoniN8
L]U&GP2ehc*I3eWZs2Y<^r8OGLD0yo[_`IPX/)DHB{u`JK]Ecr0*$(8310k(q_x>lQaaFDj3Y{qIKzpNLJ/L2EFX+}

rPp)Hlma7~Z]g7sa2GaFq{:KH=TCrERQ-@kD_68JnU4#3(_L;UG^x(T*j2Bjb-;w^]^5T5/PhSkX6Eb3=J7c$0VitJ
-vc60B(7AVLP!kfi+M(g/
:]+f*TZj97_xBoFs$S;!QSDPnCUnUs;oM*8c8hlV]yr+>e=?:KTI&h2]Xk%9v9@=G,2^p^3,)/;w{k#1vR_</I|nXe![V)h1HpIBxaiR/FkA:HItnaNb/kz
W3/2z-_:l"#G&vy[qv0O<$s="ppgEm?6HA}?fN4T!b#w+l_c*+#k8.=N%]#9%Z1/npD;k$y^RC~7+cTp/ZUYrv|pjropkV`x;mK%"EuB&p~@}VJ)[559.S:NpiVFQ3kdqG#Xk&4K6lvxpQsrz7cr`QZIOLH`&Y63i3oGygoR+s9,3.o
XB3s@34oWa,8hb<y=RyL]vsvgAGx?DL3~E0X?@Y]{j,k$YS>.;#c-)Apf91P%NI&P.9p!<&Z1LxqSw|P(g]BPGa6|H#&`md?GS"nA"Hth8H
I[{h|EKSF`QA*
*BqVaI{iID0iL>0h3ZX25%!A
*J71KXy:#1Y7Q4<*vr[YFl3;LhLjvH34PBY&"B';case"sr":return'.c0<%aLZ;/eGDh=KCJZ2f,d.~&86wA[N1S_<cZbr"ZzUHQ:AtDfsyT1Ce(~,+u~X!k;rgkDywd{R~6q"zWv7;bAl/
dkT*#xWRmK!rqf}w/]"meG6nsm27/q*v[ar]_].)1?RM-5)>kH1TwU~+*G$H3kZgMM}m20JgnD$_Oy*5}k[OFV9YnuQ4iS@AU2:^+yHHyMjR
FnaRa7"1^Z.E_]e#h.IVB(jpoPGMUw_ufrJ9;(M^VwMwqB[XGzFHn#pypvI$?&gwLwhw`G70F|pj0>m2gM4~_nUM)rCw_lJSF>Y_
G!wIz(FpW+c.}rCHbk43?I-qPJtH-,p$aL,Z%l|=UVs1CPyaSaj
?(ru)u(8^c9L4PI"|Q#?r
f9^bc7Q^Vx*&srt:Dh
Zw7O063mNo#bsOWeV8fq>ABy<K0wq"U`1(u:*j>Yl?[TJ:jFqk]O%~&L+2Gv9)X]d$[,_Et{v<=18DQ_=@8yEzv=>M9|$o-zuD)
l3dy]bKU7}MHY0`m0@0I?9HCr}fUYG_^wh<ru|d0IpE06pPtdIPYG4@o3ciRfCi{GzkFus7^W9*/>&msKL8A4M@.0
wC5sb5#pdx+AdTRQGvAXj"]{Ky$}e
%l+SZ4E8/vINs(EXb:ibXMi6m2u@.R!4Y$c9(yH8cCM}k_I#NW@wmk2y`
^}?wU/ktFz"d5ueOAfi(L;n9rF:Q7!B;[E
3dSU_chE{!P?fa=#Ex4?_?VMm*yfzTrLQL/ZqH
EjHR*sbt4xRQ+S51"3k"my"q*cKMfUICeqabMuEglUkO`Jv?@M7DkUP`X~W!
HD~R@!-xxJXM!I3!io)[}W<r!xAXCX/>m+MiA6Y%}Iw5P*vCZ@@SNZI;nE6QlrDw9%MY2#=3lCH:DOv`gN@,8%AA)$63{hzy0Gjd%LWOY2[*B_3qZ$"Jnlh!,hi#VwGNKdha{njC
D<^Zy=e3(wfQz#VPf`7!l":ki9mUi
)K19VN3QFl4dSQB%Z*mME,r>f!>K*^q7
.&/1,Sb.Ej;K)9Thd<RSuqa[n.8qDfqNi_TaW?XI/4Re&9Tz(c=tUs9i0Hm@ZR+4cs[I+31*YFz0:wJYIc[
[>,u!A4DO`CpbS>)Yn_RyyeIu<g+NPemFt]3UB4EW6Cu}Rr-9Hv_=(YK&RIM}XLdQN0duSK@OZp-"MlKV+{3P-A<R/0u#8Ohb_8dF`&DG
lw,yktFt*41HX:l$|QgU3@iXMS-"BbK49tEMD]-htjsK+3lx]][
T+Ld?V=-w*u8lJ|&Tp0tf(zl+?t$e*;fuI)t6"z>_4N9X/V3Y-"ZiaPWZ+~U{EHmgAPEf`P_*&@<Kg(
z:o"K1~_{teZ{So&!o+1MI*[Z9qR#in#V):y=Z=/I!gf$#zE.Gestx15k9&w^;Hs@I+>Y0x;U#5%8;jlXf7<MdN((f9c31Dor8,O#,mN)%_Td`)C=EiMnslb61#A(oj<243&d"gc>`YM?f-s`:))/,FZc172:5CqA6fRk?D[8&z[)bwb#$lR8&I"AQ4dt5aU_h=P1X0jOHGE?k?3I#FMYTc"P>A*-UoSHArar
7Pbjide%0r*]hvM;zVB=sv&I$Jnm|)pLyfwB*gS=5"><$9MX02"0g=Nb2@~
}Jo%uZTd9&,)c)?;+;Rh{Br?kdCdECj3%<"g8A/P%wG<p3u]s>!9JA`8wrt>SFrnL7>kqMa5<
9)l7GG&qw>qN7],^#^d[DTE-,**Lj:Cdp<D$$<b4~*YP:nkF
R$H~-Np}NT
(LkQsZvtge9c;<%NRFtv=xgday+R"9u/|@@Kxd[1yf;J5fTZN+n3Aham}?ZY$J.0Y_^2Tj;%5p.c~E@lenf41DguhgLf!xn*pRh>%[T:E"&$u2
h1*OOIkyw*(VO5YI-Df=<r(zl{O6vY>omK;~-x#Pu-KS:)q~+y.th%@~G.
zGnIok=*Q5gXy`bgE]PCw!y.b`%1orm?xFDVan:RK44[FQRETRkw,`Fouc#4TKHLoBqvn-I+XM|
;Hhy"ey
Q
7iIMv4mBA@^Ek@*tk/l?bj$B8YFnrdqSJcgilH:P~;cLNNcFY0dT_bZ=+rb+,;J#&$@Mh)0%"[l_NlI7)F(nkS6OkJu;P%)N+u8]no
k?1IHH*)tQ8OKx,u5,0]"L/bGVX^k">xi3+r3,bTg!s+Ct&$SxPo8!kWviBv4cG":#XzD)9v[s:$z%edr=P-s~PwEo-_v
l^j:u:.EUZDBu"aZAc5>hxU
uO&}jEoD>q>~uNq>m;]v;X3gn90o&CJ@I%]sAZ^HW|7>o:
h?v#X&*RRPrFY/i1eMU+t(#yM%09|`EEJ]JZ%in+2%dOR;d+"ay0;6KZ0gAZ_Er,pb@1sLHQu<xy@azmt
yT]u"^0mhW<9P#pYuECRkl.aJr4Wo_">R><m0b2``IP@?S$D#.5@!i&Cn`]w$@5KThAmkL/S&m.B/n21an[p~4?W7,Cm6PoI6;]N`M>k#1m%:sZhHJ|)mi)Dz
Y,swcQtY!RAf5aPL
$32z.0;z9ccr?U8Dg>kP)v4{y^7f:JTt+;i~sPkpAr&uBmD75QMQ]1NIp=T:dY[6S*iYO5hYJ98l.%OmO|E!&T+ewONPG9MQIV$#7OuYN1bpM?ce^z*7R@`kmK9wm=<$)hYf0>+|=0/bb2kNP4pYYZ_9bi%P`oN+H7.Ra4Yz5eUKs`/qGfnpE|q$wM5L
U`v4]hZNtV>L@*GTg)~T.KW[V@pY

mbA,|4(dm/Rg4b
[0lL)p,Mn0A!Eea"N2
KCqyC)$J#o/7z%CBsmC<co@+x:g.U$r"Yi2
>`W<vCMH0*5yME=oon?nkaY_)J%GSiMd/$&n7=wA+v;I/lw3kuoJ!kngtfxjY_c3CF4MCn|$*jth/"PJN$TJ+)&G,E+T_n~++]MOo]~u8n5nj*]34gN_2,K%*OH+fB?<sUlm1+(+mF>Rek&e]2Y;dx9H]sEsOjUh<KscMq1o$C;^vL;k;-|l*K5q#&b;<Tf>F]M%#
rI^i/:i?_-+m5w9@nrl_zo64ah@99[kS:Q.,s0Boly-@^,2$~+}$MP96&&Yb-SxE*)]*YDfh|dNC^:LnNsXg8!v#>)ID$u.5.(,K2iH5bpi;t:@8mX2ljf~V/KE"&&Ou75Tis"Bh[g9G2.pO3xtv}Yy6bw{+0B?eY5Pj
t"e|cU,dS2aN/HO(P)tg;FI$RK!;1E:%Nl
AB%U@LgM0ukW
,i1t(/r?0]D~+]r7BXr_A6D;To*?m|e$Zk@#uAv(w|>~j@S!j6vbejDkJJyh2<+l3H5/A_X&L;%~ff7c#*H;Dg1(2v1+=;ByNo7}1o(0#9HSo@;Rf+P0V@rThX;_UMEQU!n"]:_YcJqi
%$p`{@SRM9>k.af8fEE/nFFJ7tH/3>EnwFsr4:S%=2K*%0Wr
nAMNf<R>h4gU!@u+/%jL1Iti/D5YDbJ"]i7Za<85.em3[e*O=/=.53T}2>+Yn5q4@heyUAy""`4Kcs(7jfie+V[<S{3k4FUj230ydPZ-s;=lIDwmQj;UM:9,yJE
1"30:`ofJ|Oxd$leD@ZNM{!&qF@VJ
TY5D6E*c*v^)ED/Uiw/EA};uN2aP2,-^4{ldr!ML:9i!-rjh?,LX9.,W2cUY@[[QbIgL2<GVPC]"W*Qw=rB?&Aq?CX;:8$]`;ym)96ZO1"f%w[6!;ev((Q@rXsM^Shrvj<btXPHI.~^Sj.oNxd""';case"uk":return'+ev;:f{p])Q*#x*04`.-dO`n]*CP9v.0L>F?9-&)./)o4(Q!ylfLZpPNN3S4mrWu}=O=CcztUA!M;q3FRJ$roUM_Tqh3K@o)1vr$
i2uepFj}jdsNX95qV
V@d%Fi>tCB,MxL[,p[ylQgB
U45bqh_@q4sB@r[)^Rj3)9[9P>a%Ajwqf%4Naqm{xA6r@oD2Jn,YE}C8)<gHt1Py@rq9ylVp_cG3!>MeR=Ld>lu!Dgx;?8:sy]E2D:C`N}T.)0y<UB#6TUAtlzsj-r){k:]:XM=-.8/mI
q>vG!6>[NN^o`EU4&s:L"y!5^CS=T1J7DUF+
{esb<W[aBI;$72!B`f=&h*g7jXDm{iDyQ"}$WIM+QP2;py&#e0"Jo98b?ay-F2snw@7G
T$*}>#pVguV!j!l"xzrRU-%P*ygbJ~uXm:-aZ_0
8F]QS
yE(<D+iGIs3k<*"SL;%GZ(3|aL#u0BGQbrv#dpM-gR00m3n|2tjs7mDmA9=`m|+9R>b|_=fqwbO=
@l
qt_
#YtA%g]rXxcs!t)-*00/yF=ZBN5yI6JQ;t8y>dnYY>t6>J!:NDpz%*aS;]QwN=gUX#"!AV$p9*^V`DS[bOFB8>>(eBq:_>5qf7NP:OYz.zlv=jg~.v0Z(pY*6{P:K`Ibpx:W_4H"E%"l0XmhTYPnv=;dr%V7X,wI`J9vyhaa/|NY^[62@w_~=FVAG+_c2mB4M]38THKKm*!T%If"4ro,tF8fio"[L-C.)BYJJxQKqwYbF$=dox^LG2-{ymAf1x^p-cs}+8[}@e8+dtmp*Fhzo(lTH.&1;R.=Hk"{q1<4ttp~d8pBssZROVW^0.@7*GJCG4nA"Xj#?45>;)25@NmE!qn.$wwH^XOtXBP]Fj
<`,bAX{Q8Ir&+?nYxm^AnC_qJl+;+A`/y)*imIp>4PJ[^tS#n3y;q)BB*)5x$[t?l,*-u*RoXsr(+:O>i8LA%;7fbJKMr6E/wk`+00ee9#;9w0ddeh9g_[o#,TV#IHUJKv3];2"c}YQgJd?<Rms/fk+(2eRb."4q`<K)nCf+8Imil3y+8&9:wdS?_2[<XmY[/q"]0A`[7=u13;hCJ92?sR06ois@L+L9|)$bF*+Ci2"YS4^9H3]EO9<^"%ch/xw.,8X7pb4obm@1oc9USjB1OLhGT)o*{]"dI!i!qqJep)(CdR8cx+VK(tOA~g+F>^G8lX{@=N%Vh"0$tnUqKP4n=R:KMSn1Es$Y
VW9x
ge]y2:|32QG^0r+lY0LyHWa:|m)bg2M!VtCP644y%yXo$CJtfV(0cZ4^bQ})JgYdm*>^@K=jRS}Xc"<Jh^Z4@[!as!Q#Gt_QZZuG9+2qQHa3DVCK;NSh4sM6?^O/V"rCko5U*gXhxmNOl,tfVfx*b!*yd,L=C#H(
V[qal*JiL3x>8FXq;2B=-q<&[d^kvrhpG7"=YDeZ.
T=7|1!Wdt9.Y_[t6N1a1T8S%XgL=ZD07G-
6Kr^]3J_=Vg!JQk.BwXHGtLLUhqBaK,]aP9Y)7i/7ogff,ou|GmwvIFukeIMXMGZbmB=mRz)BNxn=N;z%I[7C#{Jkge&Xo=1;q13)#
.fht._Sdm<@J"Ny<-|/3*sfZK`g~J<=C51Kwg+o7F)d{tNE}ihX6LCr#Xu@tw+Pf;kNqsbH6y{n3>H55*PNKbvM~/z1ulm(G]sS!lrLs&QO%?K,D<3!}8@i"#[Z{;I3K*_tRLbd@Rzf%.q-@>Iy$
-Dib:QYh~DSgkuVO^Ua+SqxTmUn_|+@`0-|4(a0?zX-2u_uUx-l=AbodLXFr/P=
($mK)Gp-2Y[HpH~pQDd;yr}q2:@&f
;WA63H
jD<IS7[)HjPxiGyf6[d5O*$z*6bwRh#t+(4)BGcl+,v*?+<AO}#^4B;pW^3Ep-"nqA1Q#+z$"([pyNU3L*ijaw@6^1U`PEBu0$/*AP
/$$Q+G"eRB)8VAD;WH^"{1Z>a7^8aOet-[T5e[S"Q_mk1j5Zh_f)3OQ>:m!eP)Q.SMt.{b0Hy2*&6X9/[a
vn`o%SYu*$-IetSv@C9nJpQu<@T4^v*Hc#fD@AaQlKf<dXXOCBg@$oBDVH/qR#H=D-bAxEdh:EUT,9.Iu@KS,@Eo5S.i]9>2^p"3bQ<U>!.V&2o>+>PR-#TSm%kybO6f.&eDC,6_C~B<auZp2ge#;n1wOu>[@8JnQ,Ws
{&q>^M=#bvKTW-7.Rnq-qB&#.S=N=WJC{lv(.oZQudbPxfiY62?Zte@q(@fUuC&bs;1n-YXlsJKg5m8A|)9hjmt*%NHuyO(m
/K7gxqy9CTgZ9$#4h;tw21s(B[2i3V&N/+q2bn@t>cIKZ~FUb$vdi6DWfK*o/X((Inpr[tSpZ%>oof!z
dA8E`[7<odB9QkM8Ja$krB>h}qj!]y}Uy)cv])7qV9K
5)+%l!rl/T?>N?pA1d,_*9F0V+4.Ah]Yhv6jNZ9wzRkd<aLjB"GLDfokpR&+4E%nf$`UGkvh/B|
buYlvR:>}8`]Wm@3j3EN2^NE/>dG=tE+lUK#y3zAPbnJ`^Z#pE;&$[O"B01R%!ptymtRac^jzqEkMoAZ|Ii;klUFITvl}&[m,<$8ly#E8__$d]C3Bu*@UE6kC`h5/iC$=3}4`5iyvjp5
qNg%k{uS`F>&9FJ$
~,D`BouoKU6L?JB9!ncAj%n*/+5)#gTo
fr(W+j,T<WY#6S:<T#&AWNle=6,WtT@=.nOGx<BO9/o:KK?nsD2I-s@9AhVf)&e0rSz(q.W<+w/`;^&wVW[yJ!8H^[]/T
G9Miv74_ktur<Fe2P(F@dwm2RUg}w;$+[sE%yA7`J5ZNcr=a?KNBz"DZvs/f2
H
^d4?uQVUVzQEI}N)5ABJP@HP;Sd`!9fE1(A{T%]vQOd>
?CRF[#i3rer-Ny1#2aLT9oyi:d<meJ)QZKw-TB2^15L)~6*$Mg1llPn;"V&eaVHff*_O`)ep5P10nMrB8p5/%E
,kJ7[WHLUch{_{RUI[eWL*F%FL`cN]Rp`Ua(*y`xu5r@+Fn$_W74$GEdYq.hu#mO0PR6S%V0v=pReB=OAI0#Rq#WUzt5ZeY38X=<3cAg2,FHl4mLZU^%UYR-;Tq;_O!eV,7>!?,mPpR_CQ5`59NgvmRqaHkUu=Qu?v1eThs:i#B`/mxujthUG_PCrMc*:eqC`0v=q5fOK0o&bsmCu{T=yj_&rt
IsRDm;Y6J_3J}
wW~&JLOI.twfsSg/fV&<p1IZh/16-wF9uZ:p[l}pd.0)5Wn0qOp4aZkBRWPNkn^oM,uCYlmk,L?e=m,_7)J!F%@<Jn}25Sa$+NkeNhvWX?3qi0}c1p9e/(?U{b,Xb,Kr<P%n6[1((Vzneb3T",onqcjGD$+a}nc@BZt4mGcvdxEJtFAJJht5lmo/36/go[(Ne7vt63seMk|;)y[<ODS[mSLH>1bw+95_<5$s+q[+.iiyd%(g!=gJ/gIbEC[<0d
oNFN9~BOvbA|lofe3iR<op:<N@v
&2U_u=y;V[7tN|bw"kCR0)[|M55fu[Ll-Bxm2jt^Pf)
B-;D4aljB"NTvsWnO8b%l]2g*)2BT6g=+ve]^pnyjX(`[Krj-PFdR>IkL8Wh(&m*2.*h!$X&W;IyMHFyb:cUx^#p7(uECV&5`|M7(e[Cb3t;`F@2NC[02?jw0&>_el+!@;?QF]+Fwu,bsNAs+~IpHp?a-eOVvj@WTT%eo<e4E4sFY0IBeI4=uNR/&Yho)*K}M(yN^.a1U!cVMU3Am_787N^Lf+;PN`D!nvaWhN$G7k:dO4Bl,]N~?K/^>LB!0&B"5}%8Ds[G3jL<ZYbHOaZeO/OpC`i1r^=Os+u]+Jq-4!<Tb>Z-Hhvof,A2M7E/]p_8![FJ-Pm2"DaC)VZ7m8lc[JmS%+/z)T_5N4N$,v>JI%j8<b;8XC&!E"kf)Uun,TA>d@*&/$C0,$.$_/d;uB_,`;:T=A3,]S]jgiwQRq%wZo$$)QGQf"k%b`=+8:E<j@Zso"N5vr^,W$f?HZW7U<2I4sB9ygHT';case"he":return'$s`5q6KWB&+kgCgv9hk6|O,^>ZFOiE^S_jXv_BlGV]JGZ+@Cn2JW@[+HDdzU8OZ/A;}#P-z;niS+6tOgw*IS533h9f?OSB)%0&7mN*b9!
jbxYdN3vIDdybtj[(b~?9g+)}eSTVDV]P[$_S->,/3I[HHo2hm13:NLwI=l?6K?<jbL5dciG&OE,=P{:XQ^YA:21A_G@iMdln1)G5c;*,5Q!tq4l"yGwrs~j<HMQp$C[7vl2l`"?E4v
=uoLQ)J%5JS)S52i}#$JVeHY7l{6[6?g99%q,JDbfDq!!B.acf?eU3Om1(!44`1qYHn^;(4QJFvD`g[VcJVS"7QUI84:kX+!5Pht^M}gWnMy4mXJ`y])]q"xfPec&Fk?zdpv@FHe)j~
n2&^G*
Bnu[))54VUgi?$ei7ERNKIs1x4A8H5FpD:VPg-Q
=A>_`F70YNSDu?9ae%Kz@Z<"7YoK4BfuwRjc[rfFiHgb5m(sVIKo/n6i^obpq<HhJVjOk%hK<u76U5)Ue3RZGrRR?[">mZ8B2VEMRnoD6%pfw&B%Dy4i`{icLMe]WbS3i7[oRKw;:Y%D$t(9P#_lZI3sgY
9,T<^vo$t@*DB$%FWu"Dy=XvT4/F5({^)#CI7Pm=^MGgMa<z(bzKI)IFsoV?I4u+PH%kv36cOQ.</sb,zMv$%Q8B{PhpFQ2"xQ>a(n_D9#=GOcu`?]I&]%zmJ<(i>w?2/MtVUxpHG:kNX,cu>)7m(&Yj~1L6v]G4`oi`P]=l
>`K9F&0pnF]QQPpKMdgs#Z*q!wn1C3Q
ic8}.r#)US1$e2%9(B;xOrjeGy9z2ce+/>W`%;1kssiZ%p_=Xi%PJwuzl|%*K_h4j`[nu{o-D6cWpMK&HXc]w!PppOkM3DV(?ah1$andd5$kruhy7-K%Oyx4bI_=0~"NyvcfF2-D1i#G:bsjLATqHY5wC5Ek1PPkg
!R5VUWae9{Z#+*5_UU/gD<&DT)CzEssA
D4$%>6iK>i{&Jx)"JNZS8JKS^%5ei+zC$F4Pe(dVGnjsIchx!JqMb*_$J*_bdl}hz[73}yT8Vgkt9agK9GCH=/"llTT7qGTw>un&`[+OnACq3V-&1b,5
QMJw6fTvC-Or[z3WWg74&?+s-]3fhr1pTMruZ_f@C8<;1IlGxgP]?`7mK^`-WsKX"SMf5,7nWj:](MT?eGSbtn(Cg~^D7^#X+UywC56E%Thb;=`bQ[u24](r3UX0UB%*n[6O$It)WhFE64!"A:A"=_**o$<=(!yBA!sA#V/`O:dyb2eT743RuS0,16A+=s,QWmL^/GF8JYGOj-I=Qr6bsxf<dT7kuQ&
"p5*%98C`Deqf3"%4Zmjt]r~Es5ZP%Ogn`vX!.nCT_)u_6tnQ")T<>w::h(#+@g<[s4e^aE`HF>}]oBQr3.|633|Qf.+HuPsp*..Udpv@K;vDmWx6C4u3;o:a#k1e([9>bFtJxvD(}"m]>b,-kP12-s!C{:S!
P#m="sZ<Ul;bUXC~6aV,q?fsX+.jU=pnX~JCdv3!
Z5
R;o8u9Cn4g:9[(QSKNK}KeN5EUN8)kC.kvM,J.5^>[tyGd#iwzAKdf;hU.Qtjt`F)2i)_~=D
dt>UoD)Is=)^DxP]iAN/l4f^_!e&/PiZ!=%
7n.jrCETMkeT-/:l`v9KhvFhj4$bn4NdP_D4+`XcYf33%_fA}!nA8,V9/IqmTTkmvk[>.
9=d36ewm+r}8~4>m6`dCBk1wtbEDSgxhbsysd[gD,!::$x/uL<YFBZaG"FLi,-}x
C`]hTtIS41mr8zDi1DHKoUv+_;kCtA7%6
<*FpQTV6l=#t4:?{XLn5o^<KkWST.-y%$nO+tNup[Usor{b)[]VABG:#K0^w$*Y6GS_zTXbi0r
k>a!#IWMd$LIT8yfX@RW|V30.rKn!(J5ln@KPgxP9Vlir.yB{4Oe(d~IPHrt!OzuLvQw3[jU9hBoY`u,(
O[hvGFssp-8Uqv4rv-0FOB
6)3$;wZ4^eFsSUIQ8H1xE<hr^|@0kypGj0EVqT2U79d:l5?bA?r<A9I|UA@6J?k/4uv{$pPW%A:egnx-(3Dy0cl"fEXoQrA<B}.9aYm5ak^3[DXNU;J9m%w;G^En]:x)#{=B:JG!ZeG9>N#a1B(srE9=p
6.&kDbl,cJZMQ*E"rDG?U{Q*(:@,*rheZ
jT_%yfch7ba;=h^0O?jZW&^e%[-,#P<m${P<JPA#.dUcBeV^-UeX<HcYq-qhVj18k5>0_W@H3)f.>(UqnZF8LLknWlC$MspywEME>ad[)a`VnY5NR9SOR39Tj9bIw@w}52tBwCpTR[gU66N/vQ"@tjQU.hR}y$y55K)c8akv7f/4s?6A5J8`UGh4c_#C!:[4EBKU,BNOpv;s]n*tvy0^Sh%%
v)wPJkP>D$Z"hPb(Q98NZ.KrvdmA-Z+$(tXRYKwQtme

+vQvFjHkHysTQm_eq>ZcTnv^$RE@+`i6o-';case"ar":return',s`0qaLZ;!LX+/|%+8$9!tz:v7I:1j.o4HV
GS
8>(DP@Q15_<i7R/&5$7p?7awE(9Oh4NEu%PaC"?C
KF6vvf)4kLLn?y:$?$+ovt3$FX}KnDjY"f:,;(+Gf(jnOdFVlsPhm"m+|a+j-rFsk_oN`44`Zi?degGJ2`$MvCb/vTRGW3E4&XWt7=:A1QJXB2V,9i:T!X_)1]Zm&2wX_j]h{xK+KmLObOe,-EZ#E+HLah{(Sws
{K6H6L6p]$g<OCcLin^x_QX;DDke7dX+V,n8Uyk._WnS"xQ)4?7d`Xco_qN*8H4P4M~KrXoB4*M9&DDHIe;%jC~Ia@e+OKEm".1lO7UuKmE-.T^meRq+A[^TwEqbZ?6eD[J)ZX}r~BM5aBnMb
>=1,yU&IMFja09o=8B,eS0sq7,obC@d?>
x58%:0dopYI@],83w)J]i5*0#T+asMK%[p=&hbOfeBan.tQLZLHG/&T2jjX>0r#m!ZVe&5~JL
9X:6854.rs7C|X
sQj8N@GSdnQB9a
=
7UP.5eDSh,Y+M;9XNQcrz6OOUG)Ig
e`?e`):NK+5JK/&h#W57nNls,8Mf=U[X:_XSz7QY+,hcahCLGUA#oPPx[wH:g
!WFQ>rr&0GY_>VQNI%&<6BJ^@XHaA:u2}UI]MMy0:."WIMKHwBw1l!-WBgLoc9K9{#!6+oL^Ow@L0`pAbn}L~/,l,VvE|>]=BjkC5K9xfGN_GyRyR6^U^N<)l(~0?tF4BK9ES[=+w87mtIhNC62s<cGAd#~2VKSExAB]X3l!k.&MbyfqEXzqzYi6@6=W&y(#DK|bqy%dzy[>FKr7!:,j.F!>(ATxb[U5l_g^EQwG9G_k2y?I{@Z-%5cpG):%X=4SA7~g%pG,zJ6&Xk05#NuXla;j0M_UW#0NkGP-kg<JL/Q^^K:>UBo&M!-HdMr5EYp;$m%`u5y5!K#Lg9;XY1Sql+n"E_lFOxG*N:+N57qMS!4oE;78R[BiL03e8GDFS7%6HyCxbl^l^#vwyUFg1+_/OrdN~wl#u_9_@U:-omo[oX]SnKFya"|!AKH&p@XD=Y,VNQ7,8k{K.8:$p1>EjGRNn-QqrsR_]l0m,L%4WJ{T8CrJ0gQ;A<d%G1ZG#`%b]>;Nzu>mrr5f>fi-`j:t8F&`_Q?c(s"j.j)]"A>@<??NcM5i53CWaNqFerGtj=U=b5Ila5<wYQ`r?&0!HAX>h@#e4"#>#09]vZW2Sz%-kduFf$om9ErLUoA/K@c#X6~O.c]C=Pj`RVF>SiL@QpP4i,exwv.jjw@#)cUuGVoZj"Vq9NBP=C<>jE>;&>B&@Dqd[P`WQqs/r]XG8[bbvD`@hNNfw6`HbqzK16@IX0.@WJwjFw+%E_*[
L
g8euE9+ab=p,7}T;_d+(%NPM;KnNG2-D[np@1A=<vI"?+(N|KHtkm-I>s$ZmZ/<&<3Au.>!c+}c!C767uOP}CTXceU_!=F?#R$:eAJ77U$"k,K_$]U)V0XI2aGV<k"I4DHYm0ce#wsIzS3luokS"sl_E2,t{liF+a#XDuoVFLA,3eu-|,]64+5%kgoJ%Vv^;H5WC`aP`4itSy4owkmD=_gH*ghV{0HxFv+)1(<oxmrVdB-6)J5Eh:f>Z=)&0Z)tb$q5/n>,2:aA0:XW0M#QW:,lz_b(04(p_aznBH7-tU4SlH=iKVE2F!xM`mXKd&T4j.jmo@Ju]PLi8q!E)`I]!w+8
L~p3f"hCbc<..0tC^A3X;4iL<)yHE#&)J4
]LZNg<mFYX9DCi[Jl7!qZH5a<?r=84.;aU[(fG!-9)f:GlTm83j:vHuwT[UN#Z{-AJdGUtZxJ$0y>l%+uhxl]vSo,fNS(1=8D]v,*J2@QwHNkgDPpeeEU:m_~jk*FI@@s@[[EE0wUH<f+J]!EbsM;)NE-m<SK7.pxF"/YU]oKZgQ@6~YmxljJr3b<]UnD]E_`*pX3nr!kGeJ{=+9wowvj?Oob+Vv{`:.(i(Cz[(HjE/vnoBaVZctB!#y,-RBGx!k$
X_
#zor:FE37ir3Zt-b_(g:hQBNf9Vr
~Z}(y+J
U*+0r>LFG<p4Z2bweR*gvO@i7`"-"#G,CB0O|HwUU]s[n7A7<R>SHh~bR82wZE!5fcnR(V5j~vkLoss/.@=1M,A`Njagj;_qO(vt+vK&g=s7s<k.i(D!?*80d1xbQXd8F,F4
Y@xACTy_>z7MR-Y|nyb}_Cs@EelR_@<,
Ti!EQ]J?T"?l1e}XW*VT<`ub(*hu7?9ECvYl3arW$N-3#F-B)DK.UH{oX$UQL[?+k7@%]Qt*eGrAx:3nCo@nNr+[|Hw]..gfdZ(0=jC8v5mM$h=<rA1MA)bi-d[mCR(qZ`"x-x3y12{8E`v,Z0)O{$C%8yg""';case"fa":return'*s`/Vh".!&Ihy(8qa!Vit5i,h.lSBR/Ouy!a1!&R2NGKF-Ir7@5HWWm$qMew8M"=,#yUc_{kD-couE+iKv5X=xRvow:iQstd&2Mrpx^_ltMAeP#,]^tWWeCx;Ow+!tV,xc]=P,otgw(%Rw+G6;As<O~lfyFfYs4RjqT=Q_U"9qX>BSCp>LOkIDc;kh]M`Ms-"uBCQaM;zA@R^YBLU2{L-?-`?m;R!!>`%
}h%L?x7c~P<B$j)mZ,G!=:icgC,_67rKdT,Mfl~4U3!hO&%i$^Ic=I87~L&vN]G!YXIyR0jrh$f$"EV0F.-GH?+uTR#4iwNh25t$/t3&yX^:65h5%,Tq$Dvu4G?0WkC?x#T(FHCm)!gM"mIV!;_ghH}Mn4PO>>Vg0wa?zp;h<B!J|_6]OVc07EL.8ubHTKKV{M-p&$d6cLO:dty7{NT:-m33u4Ba,<.KJ^i/tl+HK$HW#dyOp[@BAkKCB1!0,2U5<-4MhJ1+AR?P.vGNls^UDk8>voXaUGU-"B*J9M[kHheGCjZ
Nj.q^hV#kH[9j?m>M+Y
obDBj#~[RyN3Zfw*~Ay0$XP6csHm^8,B3:}#_K{@ypFHh]0qA:YA+dRLa?6p+gfKn_gx#*"x&_.-T>rdyQfy`thJnZMRpxD70:SGof[3@P14gE@Fm7O%>fs#LdL/9b(@p,d5h<&*;BR$7W9$|:K`vr0Tz:fudqm[xsKMJT)U
9:&}0gMet/$@=N^6o`!7Id;}HXiCv$-3ieRp5>$2Um^Q]%x"*lr_kJbFh?ZzI&N^i@h%QP9UX{f,c:GopbC(%Pv:Fa%(vh!DTG(h/rr4ETF*[21_Cc;gOp"s.nWJ+[5{,/#+.#94Alz!teMRo$CU6`^57VO,s9(&6Sr,@nO2&A00[O`&oE,*V:C
@NY/(EX@4p6JkKCbf!k{%P-3MU#4j7"(FxrQvMnuQ~m%w3^k22/#TAKAaoL*O(qiRcY(OmK=A=ND)JT_YG$[2HET"ug"g$u+I?&rG5-cv.!`9gizlWQTXRe^`-$R^a&_S}d!KF:zv
kdc,,{I0jzA]/AFW]6*VrzE|$t>Jq0Kqs!
*.5YK7r/!(si_"K0mLs5v8)lunuQ=%_PPeA)26%nU<H"S`l/cy+OJ((1b)9!r)wP($x]R+YM(%]x>2*QxQVp1yh@,AL]-e5"r*upa(|e:TZ@ba+:$mJR=)HP`)cenDHWVy~rN&=mM36J<Pv$cdg0^>p9cE&1hVC`au,V_bVs=]DT$.Z[vJj(41L&TO~-p/2*@a4q?OxYkw}pT1Rwk&q
lg|!R_AH4gL-8/mp_q;
Fh6@^J<^;e&Hq0?/dxvb"?WQ43#1Vgbe,gEm)NMO5${qf8|01B&)kp@Xw,r)?(K.2TD8~qUPu.F
DFkYz*NUJA_4nC/qh/EaMFfdo"9v$u4djRU15::`~aM@ysX3KC+KzZ&T1Dpx"!j[Uhb!=4XGr-xZxq(d1i
MET[$_uXK,M)
r)jqE9e_Z1dZ|Ubj&m0Kg3:O]A`(F?y%8l[p[!p`+L*Qfj8@n_>&R]&eGTJ_i"41TMpLE0T907aRGIik,H.):g8AWra,=*5<FuA#zSh]];unf[4
&>&4>a3YT3p0EK.)-K$-wk+R
[mN+mKIWR">A,ek@PD`;YD5FFhyPnQ:T]@.}#7]&=26<N?<xZQ0b(#9Ojh<kCjJH`yDf]8s;tD`gSGu_9o-$@61wWp>Lo|gJ`(>"n,cf3sZu08X=Zl0;N*BfkFi)0OO,4dIKAt/W1xeSw7v9.[wck8E:Ze`BXr4%*g[~*w0|T:DAa6N^KgjWZ8fJ`G2j_2e%+5aS0M]JS{x?RSd1u0
hZu1UX*k2.N]ZXGk.y93V,G.T[sXIaJ<?orBDTeMobA@_
"L63T*I:|+u"bPfcD.AN`%MOqjz`"/5<*IYE&>U,Sicmn;
LqJ+7:ta#@kbJ<UXf{$,VSw[1[)Pv,3XpGliRY/BQ/i3[kBUplJOe4i4g
prQLF/`5:Opd/""4128
lE;A>L))0Fp@$?w.p*<0(Ng?IG;YC*3`l"VXn!1GIF+3[Su~J<VS1`Ic?@j`s2I%=~#GYV3_!s8Ofky{1mjVZ]4:xa9L<zxpiU8U?9MMdERwE;K:Zz6<yQ-Se?wVcfY9j3b4oGr5?l4pB*WJ:,Vt2v?_NnkZZq]]"BTxV;+@Vc5;)fKdQ8n90bMl
F8U<2VcI/_iF=RYI1>A(MQ",OG&u~J,>/;>S@*+M:Zlx>^}_!GVMs4JVjOQvfu&JS_<el]PVO.4e3p@Zg-8`*7kg;&9ED2qR*7F0upd.&[+%_Es7EdW`CGfp16jkV){)}@;>^1m=(VYm,3H
KfW$4u$>Sg8gu
<Kb&n,y>`604R^VeW_&XpC[FvKU]KMh`3(sliV=(j@hII4l=jLdAzFu]:xgSX(=A5U%p4_SOr>d:5f|
VEvyPGL#zfBRK/E])i]N_WK?JRupZpgYN/Yp!;;F{Iib,8uWM
2rE3e=V7S+12|Zf[o;+p
9cCPIw<{*v.p9"op?tYo%1)$O<E#*eRHZwX&qU1Qu.bZ_vigEm8rhRieCw*IDtSw
^+8E%ETCphB4!9@W@kdj(F#L,#+RFi9cFa?';case"hi":return'!s`LU5Lp}+Zc#Czi06YeP;
(JxDBZ22)J-hJe1!X(-s,6:[XuN|.xDbr3+VU*(Il+](B|E:!H49A{NDCqK+^+l>cJB,FG
qR!>i[FWJR73bhMk7F(_fJDcKm}@z!On9JyUaXfaWl>sg2`u(u54$mMD?W00nCpy=^0yT@oA^L=et2O`Y(%*#]H#8Vnm$8ia([ScK7~bHK0="T|FfEUlQi/knmAd=H5K[Ex_Nt3NX[Ef+`?i}jfF3vm;Cp8lxf$-,`S.hex+|B;+I8D_!I`.cc0S(Xz4f1w#qGds4kqef[C>hc*D=!l5[.rh!j@Qc
dw*]HB@$e4r`~Y~s3rEjb0ULc9PM_.Jprk|PJ+|F&Tn!<GE[}D_
7C@1$&m>r:qb
#`st[Du/dYj^X6xt3(73hywc39Y("Y`Y9<qG%-t3"kEY(1
?&/N!
PGfD
_6QeK,Hg<Px[+*X+bvlgh|ZxaKy5D0k`Z0"U=EhSpV@OEx:BtMczD[,.?tN@uHjjZM2oq{%&rryaFkma5.%5Pr,/<vis1:D^ROT93Av2a
j~(HLV46trqiy|)8S"L~P`o~0&-kQJG]/Gmi$GG>59T]<Qsn3a@tLG,vVRJpHnl+O_t+X:jS93[ma,a9^juwtDvCJke1UO-d1I
$K/;[R*PyK4/}Bs/S`O+DPw]0ur_sm*!_*h;D#2&(QJ9)]#`DQMn@L^,A8s-.T.I*?[VCM&=dxEKZI?bLe+Vs<zbi^<LnU`x9lY1x9O.RSL9r9a:zqjBl<a./&<7J#C@tcpD%u21M.EuPfzn-Hi&tk^7UktW;#fNEn$m-eFK8+1?l@L`tFjjy:06<cYiAprY]NdX!Wdoxv5,)#6/y.57`T:ulTE?Vt@OvGqe-g?My)6fofl()-QCPm7fe`,R"2heZO1^HWvwtb>9
G~!grF/(4fNbCnu+
zrM!OX$atoj$~yPpYib-+U!8+2WQEt2.RB_otwW9hJ,pP$)<h)]L*<P/Gi(j
,>**YP8OH"aMFo>ea1j/2aCs2xeem4$x*{2vVFl{i0*}[U)+^(ez&<Rur
),iLjK5Qp!3rZ.*+
lt{7,CpIB6&T7uI]"xZJgd!Y-ZW6E.%429,]5];O_vD.Lvx;X49*2;G=oC]bJnuY^
9pmP"U1]`]%6axwX4s$Q[.;um&_2u#MlgLF/v^YiK]i]3w!MbWxXbbEy5mv_`aOD,f0;&ox7~Ag!VR_[a-$tI0n1U1h#F<.!Bu76BQ$CPf[NXW7fQoUM
,IrE)GS$$Z?;$*YD;K^vkc@]&w"jCj#3n.>WK@-9U8L98S$GphS.0&Z6n.qr
!
I-Seboe(Z>
ZA
8i$=D#o1(^lC%s8k@lL#$r
K<#o5]8C3gD5,8Y#H>a5UDkw>A-yPMo)pcssI6<#ZB?&39$"22N`eAP6+rJ?.J`E)cQ!)C=Dy=-L0#Ulvj_>VZ?w7<B/#;N[E|A0^/)a/jgzk>0-x;_&s.=)6
VS4iO%EEa2_;g?XVK1WgR=T~G>Gb.+ne#o1%y*eVN<SO+-h+bUU{)Fd>7/8
gdXH^<umxYf2uJ)O&ZM=[|qpEl:bF(l84v,lEH[,0vmx_+V2nwF
mp9W!e<1K[Au#l<97K%?9l/d-)=d^&6O+#a"*MrM8e"RJ7VbQWgZ]+40Ni!_A}Jg<_c[@QO:S[]5dZr|;_-gZ%CYclR<Lr4g@aD&0<eg1o:FV~4RA=hVaGcfE}E:4[fv"wm$I(#me+lKrd=[rRvi_^kW[8k:"a#%k~K*f>6~t|NlKs1t61Ak`LT}A~Li",jY(N:ZNG.M/yD9&y!"Nw/[NU`5NpTNw{%OKgcQ"v8S?pT8V<%TX,S1vX,T+sj*l
eqeCdddR
PmP"S&9OWl2Zl%G]m1(+ljB;d0`krj_rRZc%5&4K.aKPy=.x^kv;)<N6LW;BhoK]JfV>PP;/9+#y9$:PY[(o.2?R&U}mDY8/
Q1?90Rf96O:1RX^R<EGu_z[CF`x99j7V@.bl$jyk=a-Jq^4}X56t=w8v=[]h#I8r:YNZurfy[1dC5>yP."0BxM,YoPO<g|_PZIKSo,4.IlpY6+Py5HdCJ{wgYYW>8q4>Xps|`;9moHT7/qT?tyOaR:v`d9FwCa5I@73UmXO+pW.l]JMQ4^fwS@DFXp7N(P;K7MA3S
ce_.T?YC>A[~@Y
{DK)hG
$o/HvpeY8=$(esN)19_-/eTk253Gu"y%&zh-A!Q6U|MHgTq7G_@07&DV(Yk;_rYX2YQPqz$$
*g.,$0wbIQtY,(hhI.*+Y6gm5:#7.!T6zPn8`;v9igjm!,@Bg1^l},v<o&T,k7)o2NXo$/#
GMOt|9<[J`/WYSm9tqFDoiG7}EontD<n(Ofh7K2C,4@x?7!s@cCxbh}2[:G?y,d$a*QHws)%*km2wodVQb54A*HYHLG[+y.ZMm[
D^N`31i`dCwMKql0x
`L{_d9TB7geBV19F4CinOoLEc4QK=-h4Z5O,yL!4H!FxuN[u+QZ7"M"h<XPgGt3+DTvjBy<IfQ`.e8P`U/bh#WKRuLlW;EmyLm&HZ>VO9f~:{;*3s1#S;g
bA3~?i#5).FkS"](6G..s1q`+<LsJ>@N$,1hw|>cw+;YU^NPpA)R"HFP`2/*5HiJ/FFp>e5uqa$$8,y.@|IzwWVzITWUT[m"W(yCr8LVm&uRJJdCL!HUX@F=:/"l$Fu)rZ?I
z_+MG8/mwV`;*_Htb1HL#h5ucq-Iy>G)@71Y>A|A]!BDfV&I^mdF3HNxW^(+D#tejP]
19NF7cAP5?4u~k$(QE`;R0VfVxz2x?+*)H,.&FTulhu(hc-7=9Vo8R6tTu3gZ4!Qojc2mlL-krwUi>r4Nt|Y838XY*`"L7ba(yX=!bK0t`Z;A=fo8/=Vy``QCr`6zL,gf$aqaw!g*)mc6FZt(qh($c%jP2>wS`2E4oI&&2n-H/h.qisI3YPcVO?J)?%Y=Z[d6

dTF1"/GMqa/AaL_:1nnC5
jVopG@PMC~0lP2Uf^?-#CHt+xT35)Zrx.t){]o,Lp>it=}M;bG@ob+t?=ZyX<q`dm114Z&Q3kl/cUexVv(&XBM3E#8!nW3x+
o`/-pOHO8k{8YUPrZ6:rd_CYk>RtP.FoKrZ?Dx@Et5nHT6&PylKTP4=[ttIi6u&pP;^EpLV4us1mv#&o<Xq<Z<4LM8.5Q[S:HXBv;<w[(`z?)Q
tKkK:PF^2ZHk.%!a9jllteh2dNU4hg<SBiB3*MudZ1ISG
gPCG1.?UGh1&%A?x`U8man[B@5ABXU>{Z^+DBQeQL1+Wpis:_PwtwDeSi2:#JWyKc;)lHzb{B)68=|WCG;Qd+hxhjWdf(*TEMWPKi{<m52E=AH:<x[+W:QnS.hjF
:ax<j1.6MgGS$O:Rh<-il7#k1uDE|khY
b/cf=%dDM!Lcc(A}<=nG;B?Es3XaJ+N7sgiVH2xUC@lxj+J7-l+(dror^w^|g[2yJ~ce';case"bn":return'.s`KraLWR#At?o=aA1k$Y&]i`L$B[&{N{e4&zdZAWZlQq27B
uF@}$0d</^0kDlV1fR3RImk@Xq-97_,aH[*9M;<o0p>[jc
zgh?>b/@bhHP@h&Cx7YJW4Ac[RGIfMye_G0GNY&o4kG[h[JTza#niNw)5yH_cnq3uv^>u!?m<mlHVj5%wM~bNy-y#Y"yu&2?0LOo!`0iP^*O|<{R+cdFdvYt#]*+^qxmU3xe!37o
Z#7^[aG.qhA$hyn2)"Nl5>[t(J+.KS5K3[X|MSXvb0RB_gPLV!(h]PPOQotvo:Ziv"[`VDdBVLGC@`6nJA9U#~deHpZ]Z,1(dRW:KgQDURcjn/]NTrH9DvW,>a2kKBPm[JWc`Qbietyg,f]_+h22YT
$r5gQ3pBTix3}RJSGMkWj.6)H"CF
"$d!#HGyUh0$"FwI+SssbMG)r+Px-8rwGn=og~miw`CBi{7FrwM28@aFKCZXpT=_=>@BpDTjp2+zgLuXEm8CC9IjYmg?ZGD:rsy7j67ygt`UGg^DjV!+w0"9a+@ck_ptR?OzLZ%"Evm+#tHzqe/zcK[nZPx1L)B7G|<;K}+<"I^^_V+]5T,1h`u>;0I{lA-sd64T$XBpjw%}!;!($2JP--^%(T%un_f[ZDUc#.S_Kv$2<|8%so^jdiT_=tA_wn2O:D@N7~z$$VN,[+b@!z1~#rM.Y<d=UXqQ`V#+1T<(BX#~H,m7uc[^Ky61G_u8j$B/d)sn$_?{@3IKMr({9<n5XJL@GW8Z`DE1GnSCL1de3*IXih+}e8BP=&i
fwo}L:qW<JKP7Y<$crHN.`&MaAQk$g;EJX>]^C4XQ6r+tJ`^F+VS]
ZdhBT;C9skRx,Uh2Yyh5#74=M^k6xpj=@B[|("VX"3x[^!sK)smZ[(r%:`@Vj-LIpoFSG@A%[
Gs]>i(gs]rV`*iWkSVL>2R-f9`!8m0Cj[*,r8(c3!W=4eiI.G*gadZhb8B*YYNa5P{J0"jG28.&zXcZ%_WEf*rt@w;J%I5c$."4Zhj)b3>K65&l`Lq@qOM.Si"@4ehQ;P~K";&iM6m@k7xtfJohK?}A)-/Eyh
/Z-`iTqDTg1obMI|0(+L>:IQ^ja}&IFImnf<aO53XH=svEC57*-uKFaYBA8P3$@pta#vh7f~S{318.edxnJ@)`]{J3dpw3q/,gr$t%
tDk1=
YGI&VRq[ZoQ;v8@Q{T$G_3ad96Ql$<}x/OMwA6i*YIL_QathKG
4YyuC+yBWbaRt*B0jXB2I;qSawxeM@@bLI]%]fY:l74"bI*E9ZfM`NKINP>vh;<>.L&I
]k?v/D;j#OJFO_erK
d2FZz?6L!KV6.-Tu%8Iwe3Z(O!CaH5Dn!Pep3*D3H__C+WdkK`?ZMRU3==~U;mI05FaVKffs<[orp!^hz3ser3`.q9%78Jvz(Rr(efp"3={s-S~scQEeCJ<W.4)$:ioVc8PnKBfO!vztec/N&E*2=_]GumD_UKUN[DP`[=kXm=*;h4WK6U]^F4=J1wSSz"Ysc>>!#B,qy#6X
k}HH7tkOW?hE>!!.@jB)HdRy8GPd-HB*,`xi^ItrdXxtSk0S!NQJ
^[$j*N
pKJg!}W/?ya*R.9/wS/I#v3}2p13mH^%X}#iaW7KT~?H>SE13-?0aD_7uVUm@i^}-!AA+<Z_d.[jnl-ey+y!UQ[}l7Gp-lvL$`3V?*1|+!UW2~
[3(PJ2Re+6S<%v&3
R7*u1|430?xEdw[C<xJnmt+&r:htv*WlrTQYHgiIk$J^(Xge]C?h4X)jag/V#r.##(c5^dOZq{wp%K5,)ucY1+D?Ulh$p`[6%%qr$JYMue]>&9/%dXB59sNM
!:*x#I[j1f:vi#YH|i4e#oVC]
pKpIyDKZaPC"zF/P$^c`[tw!ER<.|;2a>,/6iJ8vz0]n|VQ`CKENS
1%gWH[oIb),bNK=v.J}F[F^HXf6;ZPQ47
s>6Bi2#5N-.K|acS?MZe=yh$h?H]iqY[F$S%[!QTG>L^k7bIlS]@S9(KukH`^BH#gh["uw+h/Qn(9AtZ;&LV9i4-A"3/|OV"]ofqs_`37JhcKoPFz&=C/l:5a:6n56B[0nF2<3:3ec2.#9
ktY)(iqUlV2.[@yn=",GN8NZv3C;*ags#!g8Uk1k(cAK25Ku"I%df-D}^0xb]i$rP%H@;_TxNl8w==8I>yT/!,%L.[s9lcn+jH4LV4PtB-Z"ST@j>|iy,fY|xtBGDdPk<<p^HmEvxi>;#Mn@[>SQ#pHDU@C8[T9#7vQ=v<dp6c7h!fd>8K&S,^Q)S,<n[[%)hetl_-F4<TL|ph)}Au&iu]s^#/*Lmg)Vl6YOI-sDF~E1(Pdyef
QaQ#n4a)f+EV7GlbcP
)%00aMk-ysoDPvuDlw)pkrBG`nT$,VR6Ppt<]#;eF
a(.zdkuN>Dp:x(l4tmlmLZZ}(XEV60uO7)TV!s##K0UC,/]][)LDz&B~@<`ek:),(,XEo>XFgSf4E7>
*@D}(pu?DYI6jYEHIS::pX&J/1Y%RZqX4p7*LufPL4%`<I-Dj_y5EJ*3kA2|^OoDJ)J"+2hXm1HIJ`TLnrbZ%kDsasK5?:?K;b$kjWw"m}ud2pnp
?&,79orK_g
.Ams5AgWkO/;YyN[lIbx0n@JW1SdY8B%v)AN5@umg=j3gWK]vSj&6Q#$0YcdI,_;/{1p5s&Q;;px7s+SK<ta.a*dbAU@9)q/M/_#covS$YX2mYC3fuQ%b7`Srk:pr<XfJ:Y"[
J@rddYW7AkjEDMc($;OkJVRoiZanx!v4625oD[[*r6b>"kd=Z>ge]pU44gfAh@^aC
qq+gCKcgPvxgw[P|x,4G4_Ne5=[xS(:)>6QG<UB};}Pg8R$@#u!s*;w@AQB%h
eXk}en<vI5VeOxr&4}@or9=b>F)#D~Y*]Gf
C!y/JmZ8#o-iyA/0$uUw.KoZX|vL,wj4p:[?pE?TG7bsV(N
MSw>g>2vMbZNV9CdY}gFP.uInD#D0734koNnSb:i;mt$">^PuNe>;t,S.&.+vX:[$qIf8Q8Y/#2yAG`<7*7f3<1=9!fZA<vk1WCNRM=&pD7qlUXpn/XsP}o|:)Sjhx",omh|AWf+/bYd6-WP&JK])/V6ChpCj[c;l:GS&j=E&:nE"b/KNB.O(mHeK}0OMLQv>S_dXXa065VDXDgo.(qX^%8w2h.<`70Lkw3#UUvCFAs:6z]6KxX[b8o^$f
xD]S}1g@JqhHw:+Sv@6I7KzF{hQy~wo37LA.7<JkrqPwqRsfivn[X_<2hRFgGEC6/o%HB/1/8nU<Gq3wy&5kz6S4&_gkAR#!(W.@q+6uy?W6D+7P6A,1F>BgE)!*@P;)a*|Dg;JTIM:;.k-yfI?opHh^FJMGBJ-Fk,"m.:nRPF3TlCYn.8{S.$]ST&w8;FN0f6Z3"OJu>IolQM_Y{bam01wuk,y1oJ/"AmW<wH]Qy.&_9PjXW-[UVmZ<A_~E[RnDYt"<;9>jvs9"/$,`(aX"W"u&c(cY+#8l$C%B@5R-R`v:EBDu
YWONaMpk:HsSNO-[/%k:9ZihC2L&Q~CIxmR<O"LkTF%7r(GIsq/4hdbj_%b$g
5yWLsx<7Pc17
za?Q0F/R]>[OLN
qjymVs7d(Se-c(Ou/yaQ
iY_vKH;o4N&';case"ta":return'!sXK*bSZ5#2M[[E"F!X!GqtV%iwaciNh6xD7M%Zb0Ms#K(w%"I#.$:o05D";2MlA#cIWSE9sRTFTJCc*;F.Q4I^EWtOFeA}u0](kSt+M"M
,k^%LzV3gvjp_Xtkyr5JlZc`wLnsyraCHGA1oP?tWc/xE+=Z"5Af;0b|hqtDV=][Fks<(BH/`siV1L6Hi.L|P-9CL#A2yd[du0Hk7/y|z)^)4Uyuyw@Qr^[N,AsD=~tj,/SrV2yu&2v:;1)|]@?xeGN&tzdg0E>&^+P]_&(A)0y"]n5uRJ"~M~iV1UduSbZ+maewLv)a+I6jdJRBLG?*wB:>?Ss(<H$D-F:Yq?hJ?J`3yFKj"Y#.*~2U=UP7UEva-Gf$dS8pj9+U@nG#2v*Dfg%&yL-xyl6$o|P/Mt0(vXp<g/wbr^qj&kU*^ha4dHt[7~gu-(dc"d4):lFxL`35DPx`BuN

"9h#LYhk7xFh2B&LXNPM4YBKyZs,:(<0NQrH8O
U6#Y1`sX#~;y%&$K?-^v^$ZJ[6wa#Lk`NgZLnqMtWH2;_vKLtiTH4b-9i?]&wB/gaEu7k2L`]hG$sho0),61A$H$6+tmWg.n+G*T9IZUYN]"#FDDC`>M67>,Iqh;T5
;e.p9Jcu"]Y")s?m=F3$tm4P/)_R"3!e=aeqBl-C%HrqwWDi/si/+vW71vr&=:2DQEoX-#-]n7:*i]DbkYUf42H"Sr}t#l}>;`{taU~U_x]c.b>e!jTwY$F%Mb$H_3Udce9X,C$NMYpg.DtN=D>w1ngV+cl=C+Um9`3Yk0lfN1k<w/=696NpQp=1SqL`Mkm$Ny!X78iH+sOr4,Vdi2%/_R,&~VR*Z_Uj$`n>#1|O>r(2-T^5|tz+ZmH[i7gF&b(:wL~QU"6K@>=H-?d3Fn:@V=g2aY;>d[(TlJ&Iu%M,Hg0Zg.?mg(N@MUF8f$kMT%]F
J&=DY7:s)rGq(.oqq2.r6a^nQG*<0]>f;aQ0bx1)w:0w-(p9T7F<l3wq[sA^hU]a9;+vAm/)pP9`!@*{bx:lr0hrvS/.tFghoyU|PZ+_4"Dv@K;o?QJ^oq<-i9
c-zTBWoRQ;.3^P8jc^nLkm-"MI"WVn[^3h*x;y}wpn;iH"XJ>G59{vE4E1)(&xqjnLjsQyF!zqJP#Q@o0/#-PcuaaJp3`G2X$Bg6(fg"+CZmt5f*o25+JxTa&r<,{a,gtf;2PGXf+,1mC2;tpS<fG"pYDKFC*k-+-c=A(>52|inbX>bt)$E!2MO,(P7lMtl.|,-f4jTG?M7EbPye-h9V@^WP{xigvwBJLX2m7-lOct
/doSwJ%!drP6O+XM&aT2OVCG#uIdSfC1>JlNesge]O/ufbBf>-4fR??<g?&~wa^{G}hA%
w{&S$FSyfO->kw)a968o,F#oY(VPHy[f:&fehh<4PFD!s<:/P-GC5m/O
)n[IC*
?XZ`;a5N&u`c$;fZms&Pb!cWS%SFD>:!L%),YH)M6tpp?oUs+.={Eug(IMu-:H)<I;%#4_dM
wHF3#Gy5VFT-T*f$#w./}V,7<9.diG=/=4=uQoV:<P
M-Z#5y;"Ja3/?SyT&X"@]11gC;E4u]E~9P&s7_vZ[P.{U_^G?fqy3LAH/*r=2eoy:WU(.
coCm
4RbfY>xUE9glhnC:9.4ai]
xeI7[VF4S2cmyCJFKUCL_d.|%"j(dB_m2[_04H"AnCIjc/ucP_yGpqjl1o*WWmhJG!Xymi/7s.9XE.n#Hyc2mkEQM(wbR
Lu5?QgUUSGG?&`hChFJylCpXW+1lUKD#jcG;6D:&4ixEuJ1o`&p57RMJjM!iQ;4H>GC9Fh+{gF:`/*Yl(aZ0,K-~.Zx`06S^L/

YoNbVOuf0|O7nO<//VLL%ZRco]#$5,-6uXIJTqUUKePYu^QbgV.6-ag2gG4Ib&M}XWW?[1"e;%_RYn]^qW4A6dX^&C#Y)`wX]nl[a43pqM;]1%oq<#x&E&Wy>jrb
UC[mufNF9Pf^B+JJ>1mb,aCf@#xvUpcJn#mZ(nv*@,1EuQSy"gy"U3St5[?[kt@u7s0%n<u5d)i5%?:e?1N#Z6C
yS}d*,EJk7tYww[#(B$fqfna6W1t
>|yI$Gl3L:y)PX*E>}vc42C=[=y9
J^r;`,uIItNQPSGCYu8V+UgSIkXxmIc6U>5iCva$,/Qw/ye?`4PX@QLxd3cR;nF5IY}E>fQC.$4gJ7./]a6<J_9/I7ztFZ2dwdEZ4dmnsDI*kdKnHMSQR5rDYdhLt+->Z@u*ALlhn_=Cvif:8svj/l<#,##Wr_%WHM=Tz-T%ZV0#K-&DOI@S*a
AeA#i*!!9^>s
Ppt*ZYgrfH~>7yTXA_7,k?fs.>D!}X::$oqp<p4%at!UzG?fA9`/t(t=j/w&r7ZjG@2=G5%<R4#na;<n80"w>uu>0GL1)A1+q*LdfDwF+QDV#7N3*-OAm.Xg?A,.+K[9`GPPU1F=tPB`Sof4XX0:qIPfPt%h]_gY0)}kEveAp/IQ:O).`ZfPrZ3ij7c"2E;5*E_pA(FK0A6Bh!`>9Z0,.>o#ppaZ-c>F}q"_
G/w]g-rV>7!tm4.lWil-"GK_@ujZ[%1ca-m*3%i4`
aX_Z9dNY(@$$MI!&2y3<@V5[Oa+xY#DcDh?pf-)}CRdoBH-&ec0?gU
T,;iUaJ*t>bJ=g"t~>wC$giwa+/4*@tX$!4E*D4)<]~8_e9!D7/#Y.k"mBU]aq9f"25S0Yu;?LsRivIDOrd.r?rK*$ALnOwx`ez4HkB?$"cmM,XT%`Ice=o#>TZs(5K#K03ZB)QM[n
h[gKD,[mUa-{IEi{YS_D,JI;!s.;hjWX"JluQrJ{;(l-abXhDEsrIMR[NCWd[lO6O
-?jbq.5M5&!XB;-GJGl>,SBgB~TapjjOQ)Y4$;obwgiR:/#D79R]8$[&f`V8Q[BhD^Y`/EG(D4ay6.=jWp4&^,IOWuXx6[Hr4wl9c"i+$rrLQ9p]K:@,:=uer^s?kk:)Wa.-_RRIi~#yXzQ*MF#[7-dUPgO@UqJ>>k9#O|"9B4V*W+
g7432mYX|Rb[gk][*4BmzQd97D1/
gqH?"_j3qLn]Y6s05%9_euTmsTGy71K0NCQ"OIwRi%N1i,sndc,-/r:l3`[8+kdy;3
TT!NPti@[9h<36&<Cx8<&[7J302pmX{h#7aG$n?4>PGOSLIm
Hw@-(G,|,`';case"th":return'+s`G^bOZ+!NXXx&LapTt&#D)^[vbk#~NC.D9t"bZQE3!,Q9%+FV2UU>YdlLR|Kyn#nA-ABLJuC6Y6d:HBi0hcXzj]qLk>rFu0JJbaH!H7Bl2Un0W0W@?[Zebq51[<hk]=B_l{f!9*@{4V^K<Whqf{=*hqk0E9m*=eQ{pXtnl^p3AGm
cyGkpDt[g[K*+Gl0+n:b&hOxAHwWhQizAHfSb]6Vsi1)"B+GW@52Ews#i^H
lUoJ5xa,rMKGS]Abv0e]/<af$)&`y>#Y,m6fviA2+
F$]wUI_,d6GNwVe.[2,*)6
G-,mboGPy#42`ttg!Dmvp+>t
;&1o5IT8=9$WB+4WF,m28Tyr]8&prV`+]>v~*UImu5j
yhn4brlhX:4dC]"qu/@RflnV2*@/F6nBX=B)a6h?Oy.[$}b?KH"2/s5vS]`AJ0l@oth()7b"HQy|F"TpDk%:h-frk/oyi86JAV2B!vZVWV#1]Y(OeCO{>L*%%Fgmrzir5QRFht`sBJ*:$`?A@sMR7|AWQZs!WIM&n.Wm?>wIe;Nieuf@0eS;aMYUNYL{?)b"iofs^.H}G^Yhc$%BasT}2^!hqena"1H!/{IMI;?t9<c1e7kd-ew&u.^!
1HlQg&h(kOv$edn9GsmNfp,>m8wW!&K.1"N9Qnf$:XMg!3`u0D}20SOYiS{^6O*_fP&fX%/@*P1F|Nd-ZN]o>ipcl^m0*@aUV*3ke$/4:)=2F6HrR<4shPLgQO+eii51I*Dxo7~*PU/%f&)%{`p*0*(X^>rd9/0",P**BIQRF1&RoFKmBQx0qS&:vNMcbN,NEkmT9FgIo-cA]i|"Pe%fXHyXS2V,6d1X`.{Vpi7R2p<xm[cvf0yIg$a"[@L(;k<$:]$360Bj9Zu>!PS0C;Pf-:"b~eVF2F!un)g!1*+2<(uh?J&s&vs^[JM[_
IEjEffD<C&W2[^n/3I+%WWgI%T???y%<~T;HK6L-L+S1D]u%18k4|AK?[HM>2n{D>:i9)G=q)T~=MUo]rcqShoa8g+N0Jpmr.VC*sqI*vG?W;Dc>GA1Ag^D._.#x^<tMN58mbxR9nvZ?e<(EIj%a_(bo1un^SL]
=e}%=&14/+7Z
;8EF;5g}6*r:`~mqivmr+136xTgCXr-dpe,C[Wc??<?Iy7h-5!-6GW.R?KW^k4AEiuEth^jRZmI~%HG/MFU+]:$AcYID^HJ%`tCWr>:#bPuP(*?resNW:Q<+EjE1H3$Nn*D"^m.Y%H7e12le)06zRnfV1`q@pi16ULdL5Uxq$#(4gi
a<5]/8I;1l7F]RRI}0@Pmls"S55HUsK5{*U0z
5vxKF6RH.s&Z2>jpX6F&4;KjZ4II2R3SN7V4}-B*Kq*hm:W!2npana9X9D(Ww_G.oB+su[~p)hY34/DuetC!6OhH~q}^HtKYiRy9uN;wM?)tWVQ1$Vec#FB[5AB4Q#8x
=A)AZNE-$_hBaGI72Oeo"~.@=Y_!K
Ce;,3r$(gaPzS3+y-50j2krl><HgGv"YU)e03u3vA_`Uy|Qpn/ARp7V~#:WP1!YQr!v@JSMlQ?s5d<LBax
*F#0fo-Qelb<MQzicdQ#MQD<iQ@,#a`0crj9_EXXjd)].DE>hY{/S[S]/exo
G6ycO!Y}[oK!<K+i
KoSvN+ddDltA,&8UVLR2t3#.6,td0sr[JaH(l]}oCT]6a.N)BGa_i]l9_6SGe<eJ/PcGUZ&w~fViH[]^~UZE?mdgV74K4F<`WMMP%NxF#J/pP^I$IM2A*(u(ZTHJ1*#5~_5pZAR!7a?*Dy2u9j>vcy6CO>+#*p="NCvlKY-dX/Pk^f(L"M:8Ueqn(rr+h$(fnhDRcR9rGi9:8h!-D]""G!8(J1rj~S#10Cgl%I:8YhqX.2[RJ]*iE0_eF
UHej%b2ixKG[1Esw$391(A=Vm_m].8O^%#IQ}ikB7Z%Arb,yPmln;Q=AUr3BT={QLf2w/R}^5^V*3$&lY,-FtMG:vY}N/N7Il%8U#i"kTi40YZ*c{2<#hZKec8Sd7JYQ
Z)5qCyPr2!M!U5<e5NYpbt%s^hoO?ic^O+PVJA#0G#EN_b`3b
;5=#,uw_9G(B]@@([A^vH*b1;{d_sK]`xbL"$
0W,r7dhm)`-#qHm/bU47Z9<^Vk-jJJM8QXeXiJeWY1"0d{t<Y5r+a~v<h0S6y(AgU%BZM]:hO6lA;(e<Q[q2T?NgH=0C08D~/}#z]xT1WB-r93MEIE7-i;KTsYxG!aV70>gDn|ajLP/KLWE1^D+nYY!kWkyvsiAb7V=Qb|H2G;pPPQd98GU+a}+zOnO];tILD,<h.e&}o))2@+6(]~#ApR;1CEE:Tx
JA1"-A@ZQ>Suv>{[%wD/6X3?!oSEirqN{v{;IM|1@cB?]W8T;881eQ$idljaYFUK5vGx`8rGDsy0hi#(hny0g,.va&;jZqqf%%T&F!pq1Zdb=Z0[se0Ijwf`?4~:AtS2#9=IgH3jo?npX"cQI%h$,sfYZSHDc>`,tf1
VpP&Lh?e_l![I,YL(;SaY&U(9/4Tm7oGkRSISsnoY7$VX+7%I?26D)u)2dMt+h6o2DU:Ujn6!(5Yu8KX=4+Pap}Og8w>eLb0L[2]ul$(l
=DN#k<oB3+pFaPPCb:<6N,TTUBFMN>opiywC%';case"ka":return'+s`FCmPZ*hqq44{-:u[$]#P^1;k^P#0SYB2+v-3`pV19`F:J669^4BOu}CP"b8Xf/&;5:mAV)Z
lsPd4W,f,AUun`f{V8b}2GvT;vM.DDcY70]Z
uq$o&JE/bM-&_WThl[6ycj22%,-C[n42;x%HG%:w7,:#:f]$;,Fl<bPoEy5+ScCcF&nfm%jOB]XV2c82G$|C1<I/CnY`
eL`
rUf*fO(TL%tX7^L{KQv5j9Nn0[#CNXBh2RrQ[%[Vs=$[H91*S>
g2K,.g+=B#E$o@~8DJJtX=_K=gp#+%|D;f^S@s&=aW_v{E-W)K5b:66QkM,P
.T]OqoU6fC].nL*/ng8HT|Xv*)W}.@,L2q#"l}"I]Ood!;w5%q2F]rtU*
kb[+udCvhxQ~Bs52Kf0_qs[K6:mGA16)Ik`T6`Kqo`;A7P>b]UvvH=gH!M+g,vbI52B?.5MXu
fQI#U=$-k!.dp:u|XSZJWfp+XN1kyG>uuIfe#+GmTA!}<huU>U?U#W={.5"k[^uw>M91V5u9Y)3t77!i1eI9,YXg`q1
D=
%UYd)+|<%&R4jr{b(+[byOS5q6{6>,ETe:_BV(1a#J2Us^vU$iR+D6&gtZ^RsOZ9N$3p2r372]f]h*0=pK;x<Iv3$L@>X/H[<c,je-g`_mh/|xsOZQmJ0g"E;MpyY?dH}[{[L2(*wlil`D4;9_rG|!KaP=J)(+{T%lRJj8FT@$)4?9jJ@4S-nT>0|;frzB%j=!WP:e]7gAiXhT"6U1{]KkKe:BnPdCnDy("sf+0"o!m)&^nC`(/e8@QX!T:6hHlkpb.Tfp=+^OCC=FG
WI?Tt2"J/X(<+6*f"DTCi"o&|y?]#prIB7R-p3q.*ijYEDJ&.X=T!(duv(8>rH4ryIA6.TvxK"ifcV1snN3XNQ*rA;&
)ViH.oZvZvusw4=LidIGf"y@
r!SMtZG=+ID4P7!n5)ZhGa-*,56K$=HzMsgby^T$?&p7+/VbdC0P+noyu<eigW+EoV(BMi6<J|J}YwcalCh6l-(C$D.elxXJc#aZQdV%fg$dq90d#ML&fF8r5s?Z_+Ir"QOPZQv(RESLR3vqQiHX@L$?65[F,,@N4afRI2Z#s^-`T@C9yy=-`n,aNrs<)f<kI{[
sDb
.%v|+,sKMnrV_yU3VNRFjX*mTk0Cm[8YS-$>!Sz%w.M|G]mgRv=;P2b<Q2FO9!5E;dJ2QKX%T|Ie:-"im(Lbp$+5]!/CL"v"fadv3=CXaA<B$S$1UoA*p9D?Omc>2
b,#vnO6R;)SN--:w
GZ>ZF5r8QTq-_P^ZCCg)/eyvFAw:-U@Xz<B6g"iCu6+T&3)igNxH;nNs8qix6nb%a
P/2k`ARlp;)Fg6N6IQfiuh+*3(=Afd0Tnjlp6NI#&v>:LgKxD%Y6$#Kb&ibrjWDC0Z%w+!x4]x82n,pdEH=$JjTufYBuIIK;BOzblp-U-r%?AIc0Y6fdgk}X}sx/(6Q
0c$=t!"8y&_Z6N^CE,k0G^!ZFw](IX>p_KeCOTH8BOXWyia$9@m*#0f?f+Wjs)F*GDph*.c0DZ-e:)]?1yb8Iac6mcaG!bBn3rN]$/dNl)~@QQ#W?p4:s7C8O`:9qx/iG]bl^";+v(Xk4ql@OFNSDH32<Lv^;Lew(g:,spcW*;E$AA"jdW$sVC*T
9U28ka0=d`,a_[+"5nr)$;s29tc5%smqvEPmK_`XpjP
%8U~@X.(9b;O<n>4gd5R)A0Mw>k4="&QJd=A)"Shl<6Z30tE/+m{ZuT(*WWMq&NX#|O9N^!,a9>/4U=guSe{134F$.]C0#t_$ofQT~qKk@<d`x6P,1[=I^o|A}@fp(;$^4MGC1>9[Je`3_J!?V;)LTq9)08iDLs9k817EpL*KdrX=qZ%Ca]ut>v%5??hz#fWD^0lu8ie^}9(+mD2G(:m+c,?tYV:`nWwk2g9/Jm;5&RZ&vMmPoO;M/bu<)qjK+ib!-Pc>Sfz4R99b;%|:-t,(<h30P=w+Md6q,J/oWI#6/2/<d//Jg$}*[8}ok:4vTKW:zw0Q;mh&"1Ev}
@p|)03J#)P~G66~)A2:Zj

bc#*S`/;l!>LA-R%nMUaTb0_c$Gw8)y-V8O!@dQl75?G:H$*oKv?Vgr3^*?]A;"PR[C`%v4iC4@>c:+:&+H24HjKm7Ve^M$TsG$2/4Yk9Uw;aJ$Z-w
X_.iETCsBEgq"HIEEHJ0`qH(lyMY4@X.bn@DJ^{i>VC/g/zGk4SyX>XHE1=CyBA2
q-%%J=m>2!&+J~u!6P,^t8%qn2Qh[i>)S|EWyM_3s%%4--,_dsWpk:Y_O,[:
uh}d`/D:G[l(Gi~nKW*^6n-w+2v
Fe}d$:]X@#U7UGWp@l](:DHhj&5*cQ_/Q[O_y^yjpgm=GTf:84pV6@|ZzZV&VY:*dVBAI3ds<2vQt4!e$mh,
]!f;b44"-90ZU7m[@m%u%IL1p}GOp%bmkhFX^34<!SB?`po)DH0;n:Z5H(07(a%8J8Zs;GV.)Xld(+ldZqd>)Pe5WmJ65&$Ham.Z)w?y6gd7&aL9&`%R:6;1wa5"/L@~^o;n1iO
imgiE-hP4Hv%)N#jRdJB`3,S:7([)c
wfh=js(8yBt+3=%9x984L2ovt;>k>
R:"byjOGU)ee"C
JKAv*eFv>~tz/pF64+`,6}t!Gds1fGc/Vxf)WU:`V158/nY!QvteqNf;GXP(T55poEiPm6P;j{TIeY;q$6v*f#p&Dul-Y]N%6:a*nUQ5cA5VuC:A?seu0l`Zj2R@TNMaWt*@Pfc).O@A>1O!a-j?ImaJ^S?Gv[`6]"Ow@L%iCF@!7uS>nIWk[?BbWv*Mb.<%($j-I/s{^8[96o&
sV_]WQw*id.}hG&@<_6l`A;_+*Cv]DI+A7VPQLt~bUa+2f8Z4}fOJx^;)sV#B-clv<m`yawaNls]Q{=/mt`HL,03b%JFnq-Tjgdfky/:;+e[KLX4R[cx#th&)
<Qkv]R
tictD@gK
&i<M2Ddz[t*
*Rv%#"@X!uuj&GwwI]?5yz8bMr4!*cSQOZA$Z#C6=+8X<se6[nIYC"&<WIuy[O@EW;6HvRZ=k6FWqz<5WppSYGUC!*=XFoqTPHY0K-EtvG)VNGW=c[?SIugWGuhXk!c1]Qky@)"vtnDGh&2Eml+X>|f|MeO_0H..n|VsxoxKo.+^4uyGiW';case"ja":return'*Zu@ag".W1*S*vC%o?gZpiLA{m%B]<N__jjp6HPdwqvxd<BS`
x#m.JUm%s"rY(Z$%L.VwT9Ke<j?u1r5-iDW/3?DZ(._,SeCw`AO!=tW5yK)S9f!=K@#d/*i0t<!+K%ZOPC~ajmfUzvpbhH-M.fOE[XD`jS##1S6N/bdpjH5*33<[]B)1BOXP"#{I)()&tjiGWAjC/MD9:p_H`Lers=0,
,]Ys=:.yNpeZ]2e"mfNjV*6nI6t`xh3-1^l
]hDNdJIWkgmxPiYo
10%]y)tfSbHw?+<]C^D#)ck1[AEoR[M)"wY3pxXX_xbj:3"x]FDCdOO@h?$r|n+vh
|+ja{!61cMxa6M2uPqQ9
jjeT*"U3Hkmqwi6Un-a.c2>MabP)pBEVkISli;oEN~y[w{Y6s4iNxc;T2.BK_rTW.T11:^q$v[6&Wta~V/mH"mt{mh_?#+snX6Qv>Zt~yquh4[TNw-5J([P#H[9hUk#<n4K%B5adLx^E_l!5*!d/v/l1d,#<?FXt23O=]R_.KBe9&9(!--ky4N<{(P+<[NdUyfXjX,cbFZ-d5Q&lP4h{m^g[ZjbZnF<!"/V,doj+4?V:QZmi,=[qKl[c8;JUj[f9v_E1(mYMqoN`k}GnHTI6K?dv!>V,TmN1dkMYM5Y>Ug[tHbcE,!Q=1W/O_:Oynf]`"kK]/@.Tfu,A*-xS8361[jcut4RyA
3sg?;ZHdq(%OJ`HQ0gP9rkU/w(d`G9fXiR:`.9wD?"Na23EG<.;A:sSy5I[hl#J~_(mnES!vX5Aqfaka2V5}Q"1/[,W-gPw7$&H.5kOBTjgmm":Os"R*va(@T8XCJu2OerEbd_fkV&FJ[#BBq84|cRs)^=:?;BX{8%KGa#t@sJCYwIN;eo-O?t>jtf@A_dU@G[Rx%bKvw@yX`2
Y6<VB[rEWy)WHLUEDvgpI4N(gWd#o
$jHKQS#"TPe?E2g8wfNY3!ZyXUupnCH"DZ6<mFQq-PxoA/)PhN=#vy8huUFfF%vg~[!2u[Y&{;ivLU!^2,eFD8;pMIq:ODTS0cq9Ot}Tla=w#eJw/TGt=3y&qqmz)tnA!&E[IS/^WPk;QXNH<We^8/xd-QXKC"xnFjF>[bLz"?=P/qttbxJsk5f@4lH1v6,w2[|rd?R-v=z==Dqd+!7)J<yunb3Zk[$pY>B3?I%u<hyVfr(bd2B;+,Di.p7@;#g^_8;.HI`,BZkoeCsbz14:ll9=7Aa>JxzBj"`d<t;X%;Zb*N[/]yFw+dS9X$Md<?H-BZCR3gGtS!w^5V2m>L{"zps0uX_1QvF4]21DvgewLVXm>1H[XW4j|u3H,N/plg_!26sH"&}dZOJ;{T/)h>(e/Vb2KI|DuDq*-NLbz-*d%+{Ctg(6$;e&HK0^^+F
-??4e@Kn_[G1AE/G%Ue3P*K">IEv{Oihu3A<1O1*8yxt69!?
.97HY",l,tWs$Xn)7|d0l(F
vr:MiPDG#DK|a~`d3ekYkkA!<?R
"Xh|_Nt}_
`dwf82#^oGDptc[Z@da.LT*V18r0t"1vt)J<Z**b4(z%lHoOlI#!4eepZ"f[YGAHGcFy1_h"fhT2yFhlEHl%7"5FvUhUem3Jwq%4eJXga*eG8f=Med7Cfm=1&$>iE}g?R&JAr])"
2%asC3bHE_pu^n8##B@:~n<<J4}H|<Jsy)mU1(HC[l4keKhF#Ry"H&RWj*(/DtQv?FvSvL#1KBALiT>`IBG$)=
4)$V$sq%#FV*2
eq[Wf2MBhQ:atp%p.gI)!7wA!Lfs+jN_CSJ%InZQm*@I$I?jR8N<gA8}c/".Dr,PXQ8t.EX@L0GfJ/qR<DrGB[V|k,"(-}OIY?C3[F#N!X;H)h$%m&5d<cBK]:Jebt4cb4(IOS
"jJ]%!a!qw1T|cjCoFG0#M%/qVH>!0`fPGpcM,qW<kw!#z)cAA^@:UyaCZ|w.-eS5lt^4R:S*A$m9q
r}Od"x_Vio!"wS228,T.+~tQZ_7_%RuHN9c+!*f>s/1a3F.F+wLAh)&zbSEf!-Bq";F`9*5yYkj:YSB)S1gke40[OlwH9<Vq3glL-HpMmeZz$,3B&0YaL#7@$Af66M)cDM/t"L-d;.;T4j.z)cU]Z.(P*)Cv%hF:nyx+ODc+d[c3c7Qjy8qePG.(oQ8+eLa"Q]c.7,N<HIA,2
IKj9lpKOB(R^L%3};LtO4}//U*9FA3H"36-iuu)$ZTKc#=g&!$z(X6E{?9]vLOR}^5ynT!n(
$wDVF3H",MH/{4WNqg)*f4Ew$f2Dp(N*X#)m7Lq#X!At#H{6GG>4YyeD[H,yw//PvZmv=VY77g(Er>gyI/s?A?30wfy4b:>SZs&N<,,Y/YzZMgADA_3Bx47WFFV^h7V..1:=hqM8o?hWbr8%GZfE1h`qr#U5ARMeOfuJ:J,aMN`w0lO9eI;w!MNrxJV,.yNezB%fsu_c=?/MKCl^OEn@LHVv/*jWBs9wq1:6{SovPY
Lm/dh6[;K0>;UNFS;(;WPJ:^diq%`0[9Q5[+E/h%(q+&rI>qQnn"TM>N!`Ci>L/U2~f
WRGvJ.KvLA)/e(Dt$fyAp!h;hQ,&M?tzmt.2kR&sl*,NmB
49H-uT?3?;l+CV;A#7ebQ5,P&n)$r&oK*I1%}&l!IH#p
K)mv=FttF-JzU)Ft=HlYbEE:@er~98]W)n:110j{t;C5Q&S~i^X9bj2sa,jBXPcMJjFPW7)2N@yb0w#]2{MQmYjo2w&y*G.N!f,i[(r58f19E;oz[lQYFqi1b.p/:!"bG:jKTs"6btVt]usG
pyJ_ux;*`xDB?x@PgR0%E4KdW+&m`g`?LQ9:;Y/TIu=i]oARd"rL
3d,0XB%x&=#-j7=0KHHZ8aZrTkC[S^-~Cx0&BZrx_?qT&UhzIA>Y0B=dMK_J_:S{HX#?/WF@%-c6Kk"3
be
L"Sy;uJqokXnLORW7W4d?ZUuX%mB!Hled2?Y^k"k1*d^(JNP
=5Y<9=Y$)xY"zpl/}yty(m.AtIXwDPTX?5/^?ykhjT>?
6hZJ;:CT;MeIQR@VUcbQ[/2x(U5;:L-jJ,;~;b7(:i5Ey4=HR#Wg3C7w2z(w9>(1qd!G<d8#"1$U!K:;q*5{9>vB8T!Rv
2ro=gnFMH0>M7k*6SmBS)A)@lj@SVxo9OzU7H2Kc[7RlvhW0s80##4vSlI-q__<~Wrf+B1e$S3kzO|;[B+poD4u~0L#;.f$At}&PT[
UVB5z6Lx},]lm(ueHxAUWyO7vukT(bkQMh9KnCk@+
zUKh
Bii5CMV:QQiNov`8L5-)`^mFxoVH/FI@qUMX$jul<^"znG
?_mM{$h';case"zh":return'*UF5h@Q.w0Gi,hufe#<=@x!XbpOBwLzvId$,p"v,@A5wA$y:Jo]
,YO@,-.pX@(hI4V9$r5Z/CPj5G`HqaP=D):.*H-^
L6b~nHgasFyDI
2GJ41!M(Oe42y[n0hHHch#q9!>c~K"F;Jr"599S|.8J_^[,*S;lBPzkh
dbdMe^pyK_),27s:6X0?cRFlY[8I?SgDwDW0IJW0ZH4pwo|IhK@q$.7tQLL;]PhJZn(h+8Z7{pAOCvFrlc1(>W(neY]*fOc!QuqpM,,N#?>n*LTf+IbxCaqYZ^$C2OV=$ey0ej.IXl
vHH%k!W6]Ux8M7vy4HKS("!GPI;y7oBbHQ>3%I_]>?Jd!3pg#VdS:V8:(U87D~.2jw-bFcE)Cz(@vE#S%q%}]:$+lC5KB(vuWm8xR(9}GKsIp_hffwlYe,E"a<BTN+Vy%t%=aMMR6%rBE:cb!0(-4@bd_nA7<u@wAOa$?+Afn;Wc&RFf@M4ga%[5yq
5FQM[OWxiEb`=QfBTd|(qb3DAA8RJjA>OS|%6HxF}#>s|0e;*f;"8wP4^ZEwm1%;d?bXriR0JFEaqMx5W5*1jdZ
;,:=)(y4<=Pt<m3@9s:xny(MXs;31euh(ZEUMvQkzr*7(jFx)K*"Ie:0dVl-uf>E"%Me[^yK56ooVUkOirvLAN7(=#x:kWLbWu=(zYoW20qmUQ7a"a:HXv(Q,66)/FEit7RIN@G.y;ps%q8k^K#d],{1#o8AoFz(AWJ%~D5#j0kQe7"Qe:]G37fx5Zk?|60P`W/S`_vcL6u!*djQplb4J:KYdk%<&n>b4"nmq=L#Ju#hL7,(lNwI(/?(y7q_K>(:3Jth!p3`&n"k+KtUDV4vh?na+yZ#4ru0XT^8Z:Zw>uqstKD$hrEw&maxbR@GAAvV]yq4)s
4i;ijj9YQY
lLEg^pqXbs?Dd9@Yf!6bw.m*AQeiz*0P!4`-+=Wu*X)siCLk2sQl5TGQ~;,HZ
k
V5&k,!V[:2h6?^e#NMS@lL7)N]iC:)NAqY}b]WS/aKnE_mWlQ35+<<7
]Ub1<Nog~@qTaf.QRQ5hSs!t]s+2fu;D,#bHbD
Df&DIa7h+z(q*|?HF&hPXI;|m
-&l+r]d?+!/NASi7M|+Qus7I()]3doTx:6eUh*,.c;EXP<N3xH=.Q~;9O|bz8T;7Rjx
!:!VJ.L*]R[)J_w;y!?S93L&56xRuXPM[][`Mq?nk%R_m#<!Q(v.F:ybtPPZxyiUY8%JtObCZP0#?hK-n9,YenqmsexyR&m7E
i*h:?-dReG3.x==}gfA}nmx=*8VbI_SD@9)2bSy]Zi$f!KA.EJh*pf<z+xoxlG6t5GeEM=f^DKtCyEbcVe_rw`w+
/FZufL/:(X!`GC61=?f;(a%KD+eoZZ.s;iqHNrf8N3V9%"G^yWFc5`N&h)N?l"$Ww.Ab1?F-4bze~X&4#KQ4)R:`60oYbOR!qvL8A;z0ETTWGV=pP:jB&2|!dmxrglfD5jeS]DYZv75>V_XSjv,r!]S;:Ya
MUSEl_=4=%aDJwDJ3eV3@#E#vWqe^m6gWv:-a0J_
s@WBq.DMIIqbk(*<W_5q<00?AKT.X|#d_,UJO-GBXr$PB#4/^l"1*_EK,mq})N"1Z#P@IhK<z"!Efs(Qa#r2BM8vk
8zp_2kkVZ1s/u|.9>ldE/gG{3Uq?igL|9?;(u@`Q]#4ZNvuGJ;"8Dqv:f2IsNMv&rH0?m7";ll_-@3Ieo}lUQx0y-0hHYz!]V7p"Tg@KggD^(MxD[~"41++XPUNAdixF4l8pL}Sp3/E.S2qPA[KIFfe5imL[CKVEr3)*OpO_Psf!1%ugQ*O-iY7$ix5}G;DmHK%"u;-$(|
FY8S]tx)}bNEAd^kV.#QD?4^e`w9?v}g"j-[so6?:c"lCV+JW+%;K.5sQ;FF0CQl]P)UP"p(,ctY1i_$jOYNI-wN/!iiy/4r)1E"y(U0ctJ,7G
7=)vwE5w4=[]R/JvR@I..zY~O3cFD4!&g*h%M-FK`h<QF/8NCza&[Z$UgK4,,9P.xfKo7eGu0vtF!7:8"v
M^MC$yhn+UgeSpMIkqK)fN`UXhJm9(H1[rvCUtoR6fwL}/=-{ob"HHI
hp$dH%y>%?qxqLCb{a[#~x<T<c`JZbN.V/EIP]?NF+~8f)}cYf+-^Z|!kDh*-Pm@g;wa$Oh_[?4+jlOKp!1cT1F8P_?(:3(@?+bKI9}7N3;[]ASd*2"Xkxwe-U_),J{C{MDckvZH7cqc0q_t)D-(zT0%)paK?=@;D.y<s>lDen]`?;|++1.%P&}1"N/jGl}nb+XP9.vk^c<!w!|n"hDxSf7+*u,$,`}I%gG;/){X<Pv=>w3q|1P9(ltdD#EM:>%=n$2naJMn~_{8^Jm<W<#+@c1`6wW^?@)m6eheMtpYavtv580Cel`Y;vOFqirwK_;giG.h%)U=2a.s^JXq@H)s,6UUAaQm#KVQguL$
`cwP_?+~^x]*d6L_v}$Ofy`q/$rn=0wKR)ci$gk
CKZsiYV1`yT3EO@ch?g`wLm?ax0EL:oRR+mXA7R^6enFpN3=Yv=m/>3^j"Nue-;Y.n<IWEC
Q)S|RBAw#f*r2Ki/8r]KH6o<^C3G%B)gTe_Y)h3I#@vbIqmBL@BD]j,X(1?-31_a0F6kGdUs4rW:27mTCf4)
I]^7no*8=e>"tJ%RjeQcke|%<oOC9].W0#bhM11`f(I;e5UN?(8"XGD#pt-I45AG]C*B))djGE:p+DP)f<d3]..$a>+)Rivi~r=s18`ZT!NL$j5Dc,_g&ZpSW&K^}"_guYZHW*U)mB;B*u/Wj(`c9!x-v$8AkC?C0n`m+2w%yc4;eiayGEA<"6UX3J0n&pxExug)@7-4i-.jY
0&Pd_yowA';case"zh-tw":return'.UF/NlQWr2<okP:^E?L/8S0W}oYE
V#%ZY(DXUSQDIQ;/g!1
/z4#!wUW?4!K8DTar3Bv&n[3Dm].n.n{/iSV"T!_1=S:,r=MQC;=W[2`E%5m?#G>Xn]Jb5Z{Qd<8y`cW@{c=IkB;i0G*+e6Z"*GikZW[LdK7
f6h6EuS,G`CQlI}E;IL_:+*xB[9

<+lB&kE68y2]IER(lCT8[X4OM0n}cO%F:q*5efAVxR;cR|8_Boo+:@QRvLmO+/jr.K_GaFtT
Cn}3?-5]$]V(OnbSfr.0hm;xw0g>SK6_,n#EuaQY
;
[Z1H#43xNX_4VW+**(S?bn/Nc_IT65(Xa{L8%*DWbYt3LKH620XF%6p`6WqVi:Rl=
w@b+3;(y`dhn4Wm%w;Mn,-I})-,G66U<W)JWw3umK2^crI*P0O7dG_Up;s8^u+:
]J-FN"ioZxDTy4Tg^a?qsOE;GHQvM@fbXPpWItk0A9D}G2alq`w,KH5d26vHrzJ{kOq8p^rY=0LiLN$&V=
IDOZ5#R61kD;V!Akf;>/d-W2YQc>O)tmhl3>&-zj
jK]*amW=?hMhR|gyr,3rlw_G<MkZ^_=]Zxr_O+wP1lL$](wr){sZwgWfmv^=yILEey)ZM&vMhOW?i*kg])ZXe0Js$Y6W/%f97(8~XGNBK1l?cD%9SPUr#q.S@Ch#^#!E6+&z?XP~239~lEg=T(9`B[Fc/e:C8|i8%
cj)#e}Bw>v9rc#Qq[^>{k_Q>:tXGbwgSV1f4quDbnt#rF%nR[xORkXQf<lR6O#18=q
Z@ajKuWX8b_YU?7w{]#8xr"x-l77L$S)n0}odmxnXGU,DC5O35CHFDMtQ/OE>N=5iykQA7e0.Nux21]cN$IEBtX>?cinw6AL4Je9h0;co7U(6F?Te5r$W*9y|Dwof[.YvtKEjgbeBM;,7
Ock#f2w.K,3BlVO,"=b_uOCVL4bn^@)"uBfx7_Y,9wLx6N#`B:U5D-j7?up"q012UI?FiF{dGTjw%_F-=J1b~Rkc+=olEFa`AW.
dz"?OHX#by{"N^I7bG#7_K?e
^IE{ZB=@qW@-7(?ZbIeXXbtCO`0/E?gnA?Sc2*T6M"g@ls?N4;sLiZI:L?Y*-#3oLI%>0CyxJQKds*8?s1H!]trd]?cnkvMCaV"k@a@_gUnEhw0`xVlNLK"Y!8!8VmJO+d?HsNK>R2-Ld;V"#W(Bjc$s;IG})bP4pph9:V#^s`@:F/!Hcge(L{ieRm6M)[qXB27zW=IUP|LaZtV<"`6]r;,zQnk#1~wk3zdH>hl]`{%^3qkWyaBxW{an6HEU),+q8^^<9N9dy`U5ikgnHSgiDvRlW644:}cWBgDzdQY7vN;Po1#}Z2MHn&JfCpmvC~_v+]sYMzr.4~Q]gwUW/c,*x$l#@-VqTug!PsXYVIZet,(=b:D/PC^+@
2t)$o;mC:`Aiu^2+H8Pu?*5%sMC@sHq`"W0;TEkvb;
77[Na`4U!2"HRY|*m,E(`pO.|jWtj2z#";r^m&$Y.0]_Nwc.<*oH
QI1LM[<5hQuEv@*k
I33j:y"*D]wWvt<N&%/J@kzOIP|9#.Lq#GQ6@lxLns<b<G0A3^t9IS5B`r8$
YL_Dm&^[ix9[_@:s%k0W(HNRrzSJJS"H>]cnog;hb[pS@_[^o(z(Lr
N$GA&%[8<)dSb?IUwG@a-"3E#WSR/o4?F5f[]TsnRAbhJq_$>DM12Nnx]C&IW)`2)r[^,L[2{tiu
:^G:w!&o&`X,n/;LU-]ZP=@veu"@?~NU/&7Ah+%0m}YY:Zab1b0[32>YtSs
#_tH/~v?/N*B^CRDf=Vkr
W@:{6J
{dtQW-gh#AaD`D{OT@=xTHUn/.GWi0z`HWP)IZ10?0f$D96iYT`^jZwHRS^o~vab3&j[`h%?O2bte4})l"t53h]9"
MD7.(fU_Mx]=?r;<&jp39x0lyo{=5SO`?OV1kyS^lNvt`Pp.hkEB;#FQ-<!qpA]C]R|D9OVt
9`"Cyse;Bw]D!8xi9>cQm,t~j`4$$pdUKWb[z)ddhv(p(1hRw~^4Rt5wl`:[7y_Zqy:R[9my@^>`>iIr6/CY7fnS#8!wb#*~9MF"dX50W9_K+h)$4$J]#GXd9o"ef!^@%:3VT7Dqx2!BgbRsUB8Dkmu_Vd,yX,SMu9i^D
Yx&.Gd"FEX]+
@,Q#NxOnP,qnwfI+IVL2R@KViZkKj2(aex9?$4]%JGDvqDhgO9IpC/577/d"U]Tw|SY+=$?y3cc[W+n2b:t5b#
(JCT>S(5MW<Lkf0RE&IIql%8*})du??9dGe.4Z^QTOaG4*3o_kJ.t=eIfi3!eC)(.R#G*UdjT*[
;kr[?cLQ4ENRMKjamwl?5+QkX3Q-?[_R=1eB9Uue&fXTA<_erunLig2Km"fQrdK^wGBMdvn|62Qu"hQ.jeLF%i,ZXe**fNAXp.Jm"3#P;i`7am7Hh5`b9ihI%3b*P|Wci;h
Lh+}*.oT/m6f+yoOj:;zS$ye)P*lnuB32C!|pIj*^.c$OPV?%+KT"gevH6K@wXV:<f=w@42JX6C8ch243nGl`agy3uu}MZN[lR,8?]?v4qWMIl,Aw="V*D!hwQK1[X>7>qLjkt[y8>c.Wkk,(&>;ubyyMj]jyDyt=l!9wAI8^?fep%b@gmVqQntnWV]KQv>>ENDlU|5_o:Gb&YDHiUj{"]hYH67JXv${,EIQTf,d>,+P,
[xU)`?`>0VG^
17ooq_]WA3QAq1k9m+J<3sYnL"1`P+t9kOW^:m_T;NY2nrSe&hX1zEf^(/V@H?"pM"7#(
[`6B
"D2H"Bsq2WsU*KTGNj@[seY*k$cD]WTHX}k}s^7;H!mEQP?dZ.4^L/kP]MRr/jfvT3)p>,"}10SFH^^f,^"RxD@5r!8K)Su;!O1RO,)XoFf{nEqw3ZPjQ8>K[7plw!o3vn1E^`&zT!K_$|/
62a|g{FB)`.m;awG$$y*xeN&';case"ko":return'.Zu1$g~pM*7R|LeOwuEhjpaD]_5GFFVp1HP"P8->@.Q`@`Q"*;Q##/}
/9k4z-4-KYYBoN)6ZR`L/f2NiLKcrQTdaV{yBs0mG4jTDTNKep=JDYb;5,j:1YiL3"F7}Fjbj
~Zl1^69xAXbby8ev-J"yDN[=3`/
!Ml_`%6(n
?EIg`pz;YSI1sY{#53g_Ql98k$d:2UhL3"iJ0&yE3ls^}UkB<hHaBF<Gx-uTV)e*(`^:#qYN4S8NP`,m@f%UWoUpjOT:tD{61-;g0kQyD%|/iy]
yTlcT9FuA;#wjxw2=>nYqIr+PKaC&iaprT<MBj6oMISX$T@%}MHLc6.+y/qO-i:`o(s0(Xt@2B^XTN&)T`Rj+Ex"vw(l_d%>x<z
sdBf8I],/.:gpoG.[cCeF,y5JnVn/G^UJ2:%2pxOJ,(FT2kZG4RP:<u12V-?uhXN]!K#hNMOxUGip>-T0HTdz+uZN^mk~/yh2J$>x+E`ACGqYJ&$v$=YsXNpHbrS
<Ih:am[w3MCcO9wqA1vPwSj%=xh7-GSmvj9"%rmv8/`|kM&*Sgp&Y^EA;6+<B*S^op"r)^^@ZhNf7a1n?BU-:89kAFZ{V);kT-?qLz/x%(#/Am<`]wmwln
w[gb.Ev=i7+kDr{w0d;a1L?/A:cZyD<+S4x4,KxIB3z5nJm6m.;^!R&2Cle,S/!%p%j9i9TgqB3:qwVv>3`qG79QEBmy(5UJAQ?$9oueJ?l(uGsdit}n>tBvH=e95h9o>Xfv$C,ouI)uG9~&m?8:zL5tXR{=U&t>5]_Y7cQ=;Rxos1(7elG!dNH9GKQ2626!L$+O)2:JVSOt8MFoL7Miy%[pZ!2:@vhBs0~aMB")[a$1CH]<cO`YUJB@>5_?bmU,2sLsCL^f*v?bnhBb&X}t6koLk7>4zBML[8TvAXZF8@Y?i,ekFx
Q|FIX2<#QD+/2|dw3[?#?e;&^`qrd$;tp>JGUzyNnWUxMa-f</&Nn{J@kAKzC.aG(o>gNrol!+u"W#lku03$yeAVHP+^e!w^@{obQ:lLG}-lxaw?g-EQi]X8UB[uX23=0o+^w]`A%P;5)U`U9f8}RtjttRIx,7^.r5rdPVQjN4.C%9LS"jy}iLrtCXk&r`DvQm72Zs*)Im(O9{ZXF+^/nC4xB`tV(=;+rfKWU{601gWs)-SsHvH
_kP&%SQ%,HQo.nKTq}^_lTu7RRYqqXpuUa25Ak-k9pug@;.k!pXIdIW{!%98*D5Js}^Q/G"HKT((Z=1,cU:VMT9hnr6G:z4j?]kO0w)yI_d!!7WGk.dg#0my`!6fvd*`p`rsr5s,h|7TW-p<n2[2!2Rkvf#-U&5TJ.r8;U[I;RdRgSCe@^JEO_Htao70KBM0U6na"xg5=JnnyZo"`-p:g-T:->`m4H[NtTt.<FCd]b(>kpL:7IOgQlvMrU&*LEQ2;3X*Npj?H[3,/k?h^cSbZpm^roYXXwC5uu"Y-V:vvWdO"Ew`ZUPP9Pnnjf8imr+x#Rd<$0Kvt"XN6Xfx,;K|aXVF6J9S1[jyvtukq%!lw&$|)Y1hNiBQ/*L(OATD^sk&WQg<amY
n!90-}tsmsv^>}k8u?XY0d2!6@O:5)-Erl@_)`"*+pibAx&-Kb+v(NLDa5l83i(Vs2Ykp?x*Jily-&_u9!i7E;^XA(h-_==I-DlBMgJciFF.^*5~v>:vQ1!s
4AqdO*A75qf^}M@JF67n?Tg4LM[A55I?`!u<O:`BW1T0wt6ER1(MF#-*z7U$EEDO)W[xv&s&%a@c0xiA0^mpbpQH(<z?SsLgsXj_Vi/p4U)T"WX>w2%Et96+mSpt]3i6
Ni!k4<6BE5VdVsVYu%.[ti;9-5J/7Pu,0n35XEZ"
+^p.6SW!OKHvJwd?Z6mYCN,SjrBVd]MNODy4hB"skpd3TRJ=UlQl~_Z_QK&AfB%;WL2c_@yQ,6To/ZQg6NqPy<9/SjP9rVcCi,^tRciZ;#b/C0<mDs9<GL!%La*.39;w}Qk[ItR2lSvLwhz0!mmg16rTxWX@/JT5mf|IeG6t3D)tiArWCi`PvGFlUkt6Owrv)d|"9?gF
agmb5efvT.SL3}*S36d#6OrN2TdZn^v]MD*pSORfEYqtt
G3j76ah)@+#"JXj+LXc)Ssf-Ap={v6N,S3n3l_Vl:jG!Sy+%,
(_B+
hHB)khJ`"rSYew=j*!o`vI:n[l]`M!:@sdFdl2bPs/@8N.LA,(U_elc)*yi8,4,>cLUqhNi(]dVrds>qq=itSk@FIB"=d7:__gc.w/TvcJ7<<*(J__dA7[0^#G36<ZfiZ=dJ+8th}j~mASD/KZu:cg+sAMsA78#<9,Jc&wh
kt3->^H`D*9`Ovh^_ER5LQwnMA[CyMAbZ6|?T96CYs_38<4k=8tSA8[vX%zbyNCp$/7=TLNa<Wy)^Wxhib5=LI}qX9L<rpgS5o9jj@iKPH!%@WL4f]s5t>7Q#("`x90dtI5yO5VTttY`ERx?XwmmDMP=N?0G?XkZr"M,iP[3{RTqIRIyL2C_X3+(fQNA)65rQ${;AWD]rk`.}Tc&W&p3)+SF^
@-MI^.U%qm/(8[vj2b~/b@YlsB$i}C9k=DcK;BU*OCoE
?VXLRuEW(KMj]-g7FgXv3rG%t?cAqWjwbHvj-4gu>$pjXDyUK@u{85GYC*!lR6ElT-ge.-@h#D/%kLy]q2wvBm-iYkD.=ik+%Zkj0%LU1gc@@tgv:3Qr^?qtJDo2H.s3b_K:cY[Z]p;wKfZALjZddAc#AQSF
F=S1Z=v@HjsV3cYt9]PnG]$m;+$P[;D9N^k<AAp(t&-+HyP[0I{V?%,khT+*;wFCy,I/~O}W.h1OC
=Dn-FoeEx[ITW]4wwxt=<-uoX!v)u@@WlW##{;5ID=Cop6WDo+Gsmx]Npl]![o=iL"(
e!T@IL],X0?xq"$WdP?N[D<G"Ax5M]jz&9U]?+oWP*V=J2{lLYzGUggsA4Y
ltpY*_gGulD":X#/8CEfIvHZL=8w.hT,$Ath0<+1RA"h6uPlM1%>hF4co`KkT0P.dDgdNcM$
TDaeV=l7Ho!6^<dwKu1R>xMO4N&u@V=X*lP+W:vpt~lp%N9}WQ-$q#QPhaR4x(ujHt-O!OhYa]IhhdSI#iv|S}v|
)g_w@:9n|9NpV9V"GgT/,=W^yxeN&';}}function
get_translations($ef){$gc=($ef!="en"?decompress_string(get_compressed("en")):"");$dk=array();foreach(explode("\n",decompress_string(get_compressed($ef),$gc))as$X)$dk[]=(strpos($X,"\t")?explode("\t",$X):$X);return$dk;}abstract
class
SqlDb{static$instance;static$untrusted=false;var$extension;var$flavor='';var$server_info;var$affected_rows=0;var$info='';var$errno=0;var$error='';protected$multi;abstract
function
attach($O,$V,$G);abstract
function
quote($Q);abstract
function
select_db($Qb);abstract
function
query($I,$nk=false);function
multi_query($I){return$this->multi=$this->query($I);}function
store_result(){return$this->multi;}function
next_result(){return
false;}function
inTransaction(){return
false;}}if(extension_loaded('pdo')){abstract
class
PdoDb
extends
SqlDb{protected$pdo;function
dsn($wc,$V,$G,array$Pg=array()){$Pg[\PDO::ATTR_ERRMODE]=\PDO::ERRMODE_SILENT;$Pg[\PDO::ATTR_STATEMENT_CLASS]=array('Adminer\PdoResult');try{$this->pdo=new
\PDO($wc,$V,$G,$Pg);}catch(\Exception$Qc){return$Qc->getMessage();}$this->server_info=@$this->pdo->getAttribute(\PDO::ATTR_SERVER_VERSION);return'';}function
quote($Q){return$this->pdo->quote($Q);}function
query($I,$nk=false){$J=$this->pdo->query($I);$this->error="";if(!$J){list(,$this->errno,$this->error)=$this->pdo->errorInfo();if(!$this->error)$this->error=lang(25);return
false;}$this->store_result($J);return$J;}function
store_result($J=null){if(!$J){$J=$this->multi;if(!$J)return
false;}if($J->columnCount()){$J->num_rows=$J->rowCount();return$J;}$this->affected_rows=$J->rowCount();return
true;}function
next_result(){$J=$this->multi;if(!is_object($J))return
false;$J->_offset=0;return@$J->nextRowset();}function
inTransaction(){return$this->pdo->inTransaction();}}class
PdoResult
extends
\PDOStatement{var$_offset=0,$num_rows;function
fetch_assoc(){return$this->fetch_array(\PDO::FETCH_ASSOC);}function
fetch_row(){return$this->fetch_array(\PDO::FETCH_NUM);}private
function
fetch_array($cg){$K=$this->fetch($cg);return($K?array_map(array($this,'unresource'),$K):$K);}private
function
unresource($X){return(is_resource($X)?stream_get_contents($X):$X);}function
fetch_field(){$L=(object)$this->getColumnMeta($this->_offset++);$U=$L->pdo_type;$L->type=($U==\PDO::PARAM_INT?0:15);$L->charsetnr=($U==\PDO::PARAM_LOB||(isset($L->flags)&&in_array("blob",(array)$L->flags))?63:0);return$L;}function
seek($Ag){for($r=0;$r<$Ag;$r++)$this->fetch();}}}function
add_driver($s,$C){SqlDriver::$drivers[$s]=$C;}function
get_driver($s){return
SqlDriver::$drivers[$s];}abstract
class
SqlDriver{static$instance;static$drivers=array();static$extensions=array();static$jush;static$passwords=true;protected$conn;protected$types=array();var$delimiter=";";var$insertFunctions=array();var$editFunctions=array();var$unsigned=array();var$operators=array();var$functions=array();var$grouping=array();var$onActions="RESTRICT|NO ACTION|CASCADE|SET NULL|SET DEFAULT";var$partitionBy=array();var$inout="IN|OUT|INOUT";var$enumLength="'(?:''|[^'\\\\]|\\\\.)*'";var$generated=array();var$primary="";var$query="";static
function
jushModule(){return"";}static
function
jushAutocomplete(array$T,$gj){$Bj=array_fill_keys(array_keys($T),array());foreach(driver()->allFields()as$R=>$m){foreach($m
as$l)$Bj[$R][]=$l["field"];}return"jush.autocompleteSql('".idf_escape("")."', ".json_encode($Bj).", ".json_encode($gj).")";}static
function
connect($O,$V,$G){list($ie,$Dh)=host_port($O);if(preg_match('~[^-\w.:/]~',$ie.$Dh))return
lang(26);if(preg_match('~^-?\d+~',$Dh,$A)&&($A[0]<1024||$A[0]>65535))return
lang(27);$e=new
Db;return($e->attach($O,$V,$G)?:$e);}function
__construct(Db$e){$this->conn=$e;}function
types(){return
call_user_func_array('array_merge',array_values($this->types));}function
structuredTypes(){return
array_map('array_keys',$this->types);}function
enumLength(array$l){}function
unconvertFunction(array$l){}function
select($R,array$N,array$Z,array$Kd,array$E=array(),$y=1,$F=0,$Oh=false){$Oe=(count($Kd)<count($N));$I=adminer()->selectQueryBuild($N,$Z,$Kd,$E,$y,$F);if(!$I)$I="SELECT".limit(($_GET["page"]!="last"&&$y&&$Kd&&$Oe&&JUSH=="sql"?"SQL_CALC_FOUND_ROWS ":"").implode(", ",$N)."\nFROM ".table($R),($Z?"\nWHERE ".implode(" AND ",$Z):"").($Kd&&$Oe?"\nGROUP BY ".implode(", ",$Kd):"").($E?"\nORDER BY ".implode(", ",$E):""),$y,($F?$y*$F:0),"\n");$this->query=$I;$fj=microtime(true);$K=$this->conn->query($I,(!$y&&!$Oh?1:0));if($Oh)echo
adminer()->selectQuery($I,$fj,!$K);return$K;}function
delete($R,$Xh,$y=0){$I="FROM ".table($R);return
queries("DELETE".($y?limit1($R,$I,$Xh):" $I$Xh"));}function
update($R,array$P,$Xh,$y=0,$Ji="\n"){$Kk=array();foreach($P
as$w=>$X)$Kk[]="$w = $X";$I=table($R)." SET$Ji".implode(",$Ji",$Kk);return
queries("UPDATE".($y?limit1($R,$I,$Xh,$Ji):" $I$Xh"));}function
insert($R,array$P){return
queries("INSERT INTO ".table($R).($P?" (".implode(", ",array_keys($P)).")\nVALUES (".implode(", ",$P).")":" DEFAULT VALUES").$this->insertReturning($R));}function
insertReturning($R){return"";}function
insertUpdate($R,array$M,array$Nh){foreach($M
as$P){$Z=array();foreach($P
as$w=>$X){if(isset($Nh[idf_unescape($w)]))$Z[]="$w = $X";}if(!($Z&&$this->update($R,$P," WHERE ".implode(" AND ",$Z))&&$this->conn->affected_rows)&&!$this->insert($R,$P))return
false;}return
true;}function
begin(){return
queries("BEGIN");}function
commit(){return
queries("COMMIT");}function
rollback(){return
queries("ROLLBACK");}function
slowQuery($I,$Pj){}function
convertSearch($t,array$X,array$l){return$t;}function
value($X,array$l){return(method_exists($this->conn,'value')?$this->conn->value($X,$l):$X);}function
quoteBinary($xi){return
q($xi);}function
typeName(\stdClass$l){return(isset($l->native_type)?$l->native_type:"");}function
warnings(){}function
tableHelp($C,$Se=false){}function
inheritsFrom($R){return
array();}function
inheritedTables($R){return
array();}function
partitionsInfo($R){return
array();}function
hasCStyleEscapes(){return
false;}function
lineComment(){return"--";}function
engines(){return
array();}function
supportsIndex(array$S){return!is_view($S);}function
supportsAlterIndex(array$S){return
true;}function
indexAlgorithms(array$tj){return
array();}function
indexOpclasses(){return
array();}function
checkConstraints($R){return
get_key_vals("SELECT c.CONSTRAINT_NAME, CHECK_CLAUSE
FROM INFORMATION_SCHEMA.CHECK_CONSTRAINTS c
JOIN INFORMATION_SCHEMA.TABLE_CONSTRAINTS t
	ON c.CONSTRAINT_SCHEMA = t.CONSTRAINT_SCHEMA AND c.CONSTRAINT_NAME = t.CONSTRAINT_NAME".($this->conn->flavor=='maria'?" AND c.TABLE_NAME = ".q($R):"")."
WHERE c.CONSTRAINT_SCHEMA = ".q($_GET["ns"]!=""?$_GET["ns"]:DB)."
AND t.TABLE_NAME = ".q($R).(JUSH=="pgsql"?"
AND CHECK_CLAUSE NOT LIKE '% IS NOT NULL'":""),$this->conn);}function
allFields(){$K=array();if(DB!=""){foreach(get_rows("SELECT c.TABLE_NAME AS tab, c.COLUMN_NAME AS field, c.IS_NULLABLE AS nullable,
	c.DATA_TYPE AS type, c.CHARACTER_MAXIMUM_LENGTH AS length,
	".(JUSH=='sql'?"c.COLUMN_KEY = 'PRI'":"k.COLUMN_NAME")." AS ".idf_escape("primary")."
FROM INFORMATION_SCHEMA.COLUMNS c".(JUSH=='sql'?"":"
LEFT JOIN INFORMATION_SCHEMA.TABLE_CONSTRAINTS t ON c.TABLE_SCHEMA = t.TABLE_SCHEMA AND c.TABLE_NAME = t.TABLE_NAME AND t.CONSTRAINT_TYPE = 'PRIMARY KEY'
LEFT JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE k
	ON t.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND t.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND c.TABLE_SCHEMA = k.TABLE_SCHEMA AND c.TABLE_NAME = k.TABLE_NAME AND c.COLUMN_NAME = k.COLUMN_NAME")."
WHERE c.TABLE_SCHEMA = ".q($_GET["ns"]!=""?$_GET["ns"]:DB)."
ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION",$this->conn)as$L){$L["null"]=($L["nullable"]=="YES");$K[$L["tab"]][]=$L;}}return$K;}}class
Adminer{static$instance;var$error='';function
name(){return"<a href='https://www.adminer.org/'".target_blank()." id='h1'><img src='".h(preg_replace("~\\?.*~","",ME)."?file=logo.png&version=6.0.1")."' width='24' height='24' alt='' id='logo'>Adminer</a>";}function
credentials(){return
array(SERVER,$_GET["username"],get_password());}function
connectSsl(){}function
permanentLogin($g=false){return
password_file($g);}function
bruteForceKey(){return$_SERVER["REMOTE_ADDR"];}function
serverName($O){return
h($O);}function
database(){return
DB;}function
databases($td=true){return
get_databases($td);}function
pluginsLinks(){}function
operators(){return
driver()->operators;}function
schemas(){$K=schemas();if($_GET["ns"]!=""&&!in_array($_GET["ns"],$K))array_unshift($K,$_GET["ns"]);return$K;}function
queryTimeout(){return
2;}function
afterConnect(){}function
headers(){}function
csp(array$Hb){return$Hb;}function
verifyVersion(){return
true;}function
head($Mb=null){return
true;}function
bodyClass(){echo" adminer";}function
css(){$K=array();foreach(array("","-dark")as$cg){$n="adminer$cg.css";if(file_exists($n)){$kd=file_get_contents($n);$K["$n?v=".crc32($kd)]=($cg?"dark":(preg_match('~prefers-color-scheme:\s*dark~',$kd)?'':'light'));}}return$K;}function
loginForm(){echo"<table class='layout'>\n",adminer()->loginFormField('driver','<tr><th>'.lang(28).'<td>',input_hidden("auth[driver]","server")."MySQL / MariaDB"),adminer()->loginFormField('server','<tr><th>'.lang(29).'<td>',"<input name='auth[server]' value='".h(SERVER)."' title='".lang(30)."' placeholder='localhost' autocapitalize='off'>"),adminer()->loginFormField('username','<tr><th>'.lang(31).'<td>','<input name="auth[username]" id="username" autofocus value="'.h($_GET["username"]).'" autocomplete="username" autocapitalize="off">'),adminer()->loginFormField('password','<tr><th>'.lang(32).'<td>','<input type="password" name="auth[password]" autocomplete="current-password">'),adminer()->loginFormField('db','<tr><th>'.lang(33).'<td>','<input name="auth[db]" value="'.h($_GET["db"]).'" autocapitalize="off">'),"</table>\n","<p><input type='submit' value='".lang(34)."'>\n",checkbox("auth[permanent]",1,$_COOKIE["adminer_permanent"],lang(35))."\n";}function
loginFormField($C,$be,$Y){return$be.$Y."\n";}function
login($wf,$G){if($G=="")return
lang(36).require_password_link(null);if(!Driver::$passwords)return
lang(37).require_password_link($G);if(!password_required())return
lang(38).require_password_link($G);return
true;}function
tableName(array$tj){return
h($tj["Name"]);}function
fieldName(array$l,$E=0){$U=$l["full_type"].($l["null"]?" NULL":"");$pb=$l["comment"];return'<span title="'.h($U.($pb!=""?($U?": ":"").$pb:'')).'">'.h($l["field"]).'</span>';}function
commentValue($U,$pb){if($pb==""||$U=='TABLE'||$U=='COLUMN')return
h($pb);$Jh=function($xi){return
preg_replace('~^~m','<tr>',preg_replace('~\|~','<td>',preg_replace('~\|$~m',"",rtrim($xi))));};$R='(\+--[-+]+\+\n)';$L='(\| .* \|\n)';return"<pre>\n".preg_replace_callback("~^$R?$L$R?($L*)$R?~m",function($A)use($Jh){$rd=$Jh($A[2]);return"<table>\n".($A[1]?"<thead>$rd<tbody>\n":$rd).$Jh($A[4])."\n</table>";},preg_replace('~(\n(    -|mysql)&gt; )(.+)~',"\\1<code class='jush-sql'>\\3</code>",preg_replace('~(.+)\n---+\n~',"<b>\\1</b>\n",h($pb))))."</pre>\n";}function
commentInput($U,$b,$pb){$Y=h($pb);return(preg_match('~\n~',$Y)?"<textarea$b rows='2' cols='".($U=='TABLE'?20:30)."' style='vertical-align: bottom;'>\n$Y</textarea>":"<input$b value='$Y'>");}function
selectLinks(array$tj,$P=""){$C=$tj["Name"];echo'<p class="links">';$sf=array();if($C!="")$sf["select"]=lang(39);if(support("table")||support("indexes"))$sf["table"]=lang(40);$Se=false;if(support("table")){$Se=is_view($tj);if($Se){if(support("view"))$sf["view"]=lang(41);}elseif(function_exists('Adminer\alter_table')&&$C!="")$sf["create"]=lang(42);}if($P!==null)$sf["edit"]=lang(43);foreach($sf
as$w=>$X)echo" <a href='".h(ME)."$w=".url_escape($C).($w=="edit"?$P:"")."'".bold(isset($_GET[$w])).">$X</a>";echo
doc_link(array(JUSH=>driver()->tableHelp($C,$Se)),"?"),"\n";}function
foreignKeys($R){return
foreign_keys($R);}function
backwardKeys($R,$sj){return
array();}function
backwardKeysPrint(array$Ga,array$L){}function
selectQuery($I,$fj,$dd=false){$K="\n";if(!$dd&&($Sk=driver()->warnings())){$s="warnings";$K=", <a href='#$s' class='toggle'>".lang(44)."</a>"."$K<div id='$s' class='hidden'>\n$Sk</div>\n";}return"<p><code class='jush-".JUSH."'>".h(str_replace("\n"," ",$I))."</code> <span class='time'>(".format_time($fj).")</span>".(support("sql")?" <a href='".h(ME)."sql=".url_escape($I)."' class='hover'>".lang(13)."</a>":"").$K;}function
sqlCommandQuery($I){return
shorten_utf8(trim($I),1000);}function
sqlPrintAfter(){}function
rowDescription($R){return"";}function
rowDescriptions(array$M,array$wd){return$M;}function
selectLink($X,array$l){}function
selectVal($X,$z,array$l,$ah){$K=($X===null?"<i>NULL</i>":(preg_match("~char|binary|boolean~",$l["type"])&&!preg_match("~var~",$l["type"])?"<code>$X</code>":(preg_match('~^jsonb?$~',$l["full_type"])?"<code class='jush-json'>$X</code>":$X)));if(is_blob($l)&&!is_utf8($X))$K="<i>".lang(45,strlen($ah))."</i>";return($z?"<a href='".h($z)."'".(is_url($z)?target_blank():"").">$K</a>":$K);}function
editVal($X,array$l){return$X;}function
config(){return
array();}function
tableStructurePrint(array$m,$tj=null){echo"<div class='scrollable'>\n","<table class='nowrap odds'>\n","<thead><tr><th>".lang(46)."<td>".lang(47).(support("comment")?"<td>".lang(48):"")."<tbody>\n";$kj=driver()->structuredTypes();foreach($m
as$l){echo"<tr><th>".h($l["field"]);$U=h($l["full_type"]);$kb=h($l["collation"]);echo"<td><span title='$kb'>".(in_array($U,(array)$kj[lang(7)])?"<a href='".h(ME.'type='.url_escape($U))."'>$U</a>":$U.($kb&&isset($tj["Collation"])&&$kb!=$tj["Collation"]?" $kb":""))."</span>",($l["null"]?" <i>NULL</i>":""),($l["auto_increment"]?" <i>".lang(49)."</i>":""),(isset($l["default"])?" <span title='".lang(50)."'>[<b>".($l["generated"]?"<code class='jush-".JUSH."'>".shorten_utf8(preg_replace('~\s+~',' ',ltrim($l["default"])),80,"</code>"):h($l["default"]))."</b>]</span>":""),(support("comment")?"<td>".adminer()->commentValue('COLUMN',$l["comment"]):""),"\n";}echo"</table>\n","</div>\n";}function
tableIndexesPrint(array$v,array$tj){$jh=false;foreach($v
as$C=>$u)$jh|=!!$u["partial"];echo"<table>\n";$Vb=first(driver()->indexAlgorithms($tj));foreach($v
as$C=>$u){ksort($u["columns"]);$Oh=array();foreach($u["columns"]as$w=>$X)$Oh[]="<i>".h($X)."</i>".($u["lengths"][$w]?"(".h($u["lengths"][$w]).")":"").($u["descs"][$w]?" DESC":"");echo"<tr title='".h($C)."'>","<th>".h($u["type"]).($Vb&&$u['algorithm']!=$Vb?" (".h($u['algorithm']).")":""),"<td>".implode(", ",$Oh);if($jh)echo"<td>".($u['partial']?"<code class='jush-".JUSH."'>WHERE ".h($u['partial']):"");echo"\n";}echo"</table>\n";}function
selectColumnsPrint(array$N,array$d){print_fieldset("select",lang(51),$N);$r=0;$N[""]=array();foreach($N
as$w=>$X){$X=idx($_GET["columns"],$w,array());$c=select_input(" name='columns[$r][col]' data-default=''".on('change',($w!==""?'selectFieldChange':'selectAddRow')),$d,$X["col"]);echo"<div>".(driver()->functions||driver()->grouping?html_select("columns[$r][fun]",array(-1=>"")+array_filter(array(lang(52)=>driver()->functions,lang(53)=>driver()->grouping)),$X["fun"]," data-default=''".on('change',($w!==""?'helpClose':'selectFunAddRow')).on_help_value(' (.*)|$','($1)'))."($c)":$c)."</div>\n";$r++;}echo"</div></fieldset>\n";}function
selectSearchPrint(array$Z,array$d,array$v){print_fieldset("search",lang(54),$Z);foreach($v
as$r=>$u){if($u["type"]=="FULLTEXT")echo"<div>(<i>".implode("</i>, <i>",array_map('Adminer\h',$u["columns"]))."</i>) AGAINST"," <input type='search' name='fulltext[$r]' value='".h(idx($_GET["fulltext"],$r))."' data-default=''".on('input','selectFieldChange').">",(JUSH=='sql'?checkbox("boolean[$r]",1,isset($_GET["boolean"][$r]),"BOOL"):''),"</div>\n";}$Mg=adminer()->operators();foreach(array_merge((array)$_GET["where"],array(array()))as$r=>$X){if(!$X||("$X[col]$X[val]"!=""&&in_array($X["op"],$Mg)))echo"<div>".select_input(" name='where[$r][col]' data-default=''".on('change',($X?'selectFieldChange':'selectAddRow')),$d,$X["col"],"(".lang(55).")"),html_select("where[$r][op]",$Mg,$X["op"]," data-default='".h(first($Mg))."'".on('change','selectFirstChange')),"<input type='search' name='where[$r][val]' value='".h($X["val"])."' data-default=''".on('input','selectFirstChange').on('keydown','selectSearchKeydown').on('search','selectSearchSearch').">","</div>\n";}echo"</div></fieldset>\n";}function
selectOrderPrint(array$E,array$d,array$v){print_fieldset("sort",lang(56),$E);$r=0;foreach((array)$_GET["order"]as$w=>$X){if($X!=""){echo"<div>".select_input(" name='order[$r]' data-default=''".on('change','selectFieldChange'),$d,$X),checkbox("desc[$r]",1,isset($_GET["desc"][$w]),lang(57))."</div>\n";$r++;}}echo"<div>".select_input(" name='order[$r]' data-default=''".on('change','selectAddRow'),$d),checkbox("desc[$r]",1,false,lang(57))."</div>\n","</div></fieldset>\n";}function
selectLimitPrint($y){echo"<fieldset><legend>".lang(58)."</legend><div>","<input type='number' name='limit' class='size' value='".h($y?:"")."' data-default='50'".on('input','selectFieldChange').">","</div></fieldset>\n";}function
selectLengthPrint($Mj){echo"<fieldset><legend>".lang(59)."</legend><div>","<input type='number' name='text_length' class='size' value='".h($Mj)."' data-default='100'>","</div></fieldset>\n";}function
selectActionPrint(array$v){echo"<fieldset><legend>".lang(60)."</legend><div>","<input type='submit' value='".lang(51)."'>"," <span id='noindex' title='".lang(61)."'></span>","<script".nonce().">\n","const indexColumns = ";$d=array();foreach($v
as$u){$Lb=reset($u["columns"]);if($u["type"]!="FULLTEXT"&&$Lb)$d[$Lb]=1;}$d[""]=1;foreach($d
as$w=>$X)json_row($w);echo";\n","selectFieldChange.call(qs('#form')['select']);\n","</script>\n","</div></fieldset>\n";}function
selectCommandPrint(){return!information_schema(DB);}function
selectImportPrint(){return!information_schema(DB);}function
selectEmailPrint(array$Bc,array$d){}function
selectColumnsProcess(array$d,array$v){$N=array();$Kd=array();foreach((array)$_GET["columns"]as$w=>$X){if($X["fun"]=="count"||($X["col"]!=""&&(!$X["fun"]||in_array($X["fun"],driver()->functions)||in_array($X["fun"],driver()->grouping)))){$N[$w]=apply_sql_function($X["fun"],($X["col"]!=""?idf_escape($X["col"]):"*"));if(!in_array($X["fun"],driver()->grouping))$Kd[]=$N[$w];}}return
array($N,$Kd);}function
selectSearchProcess(array$m,array$v){$K=array();foreach($v
as$r=>$u){if($u["type"]=="FULLTEXT"&&idx($_GET["fulltext"],$r)!="")$K[]="MATCH (".implode(", ",array_map('Adminer\idf_escape',$u["columns"])).") AGAINST (".q($_GET["fulltext"][$r]).(isset($_GET["boolean"][$r])?" IN BOOLEAN MODE":"").")";}$Mg=adminer()->operators();foreach((array)$_GET["where"]as$w=>$X){$X+=array("col"=>"","op"=>first($Mg),"val"=>"");$_GET["where"][$w]=$X;$ib=$X["col"];if("$ib$X[val]"!=""&&in_array($X["op"],$Mg)){if($X["op"]=="SQL"&&(!$_POST||!verify_token()))SqlDb::$untrusted=true;$ub=array();foreach(($ib!=""?array($ib=>$m[$ib]):$m)as$C=>$l){$Kh="";$tb=" $X[op]";if(preg_match('~IN$~',$X["op"]))$tb
.=" ".($X["val"]!=""?process_in($X["val"]):"(NULL)");elseif($X["op"]=="SQL")$tb=" $X[val]";elseif(preg_match('~^(I?LIKE) %%$~',$X["op"],$A))$tb=" $A[1] ".q("%$X[val]%");elseif($X["op"]=="FIND_IN_SET"){$Kh="$X[op](".q($X["val"]).", ";$tb=")";}elseif(!preg_match('~NULL$~',$X["op"]))$tb
.=" ".q($X["val"]);if($ib!=""||is_searchable($l,$X))$ub[]=$Kh.driver()->convertSearch(idf_escape($C),$X,$l).$tb;}$K[]=(count($ub)==1?$ub[0]:($ub?"(".implode(" OR ",$ub).")":"1 = 0"));}}return$K;}function
selectOrderProcess(array$m,array$v){$K=array();foreach((array)$_GET["order"]as$w=>$X){if($X!="")$K[]=(preg_match('~^((COUNT\(DISTINCT |[A-Z0-9_]+\()(`(?:[^`]|``)+`|"(?:[^"]|"")+")\)|COUNT\(\*\))$~',$X)?$X:idf_escape($X)).(isset($_GET["desc"][$w])?" DESC".(JUSH=='pgsql'&&idx($m[$X],"null")?" NULLS LAST":""):"");}return$K;}function
selectLimitProcess(){return(isset($_GET["limit"])?intval($_GET["limit"]):50);}function
selectLengthProcess(){return(isset($_GET["text_length"])?"$_GET[text_length]":"100");}function
selectEmailProcess(array$Z,array$wd){return
false;}function
selectQueryBuild(array$N,array$Z,array$Kd,array$E,$y,$F){return"";}function
messageQuery($I,$Oj,$dd=false){restart_session();$fe=&get_session("queries");if(!idx($fe,$_GET["db"]))$fe[$_GET["db"]]=array();if(strlen($I)>1e6)$I=preg_replace('~[\x80-\xFF]+$~','',substr($I,0,1e6))."\n…";$fe[$_GET["db"]][]=array($I,time(),$Oj);$bj="sql-".count($fe[$_GET["db"]]);$K="<a href='#$bj' class='toggle'>".lang(62)."</a> ".copy_icon()."\n";if(!$dd&&($Sk=driver()->warnings())){$s="warnings-".count($fe[$_GET["db"]]);$K="<a href='#$s' class='toggle'>".lang(44)."</a>, $K<div id='$s' class='hidden'>\n$Sk</div>\n";}return" <span class='time'>".@date("H:i:s")."</span>"." $K<div id='$bj' class='hidden'><pre><code class='jush-".JUSH."'>".shorten_utf8($I,1e4)."</code></pre>".($Oj?" <span class='time'>($Oj)</span>":'').(support("sql")?'<p><a href="'.h(str_replace("db=".url_escape(DB),"db=".url_escape($_GET["db"]),ME).'sql=&history='.(count($fe[$_GET["db"]])-1)).'">'.lang(13).'</a>':'').'</div>';}function
error(){return
error();}function
editRowPrint($R,array$m,$L,$vk,$I='',$Oj=''){echo($I!=""?"<p><code class='jush-".JUSH."'>".h(str_replace("\n"," ",$I))."</code> <span class='time'>($Oj)</span>\n":"");}function
editFunctions(array$l){$K=($l["null"]?"NULL/":"");$Xd=isset($_GET["select"])||where($_GET);foreach(array(driver()->insertFunctions,driver()->editFunctions)as$w=>$Ed){if(!$w||(!isset($_GET["call"])&&$Xd)){foreach($Ed
as$wh=>$X){if(!$wh||preg_match("~$wh~",$l["type"]))$K
.="/$X";}}if($w&&$Ed&&!preg_match('~set|bool~',$l["type"])&&!is_blob($l))$K
.="/SQL";}if($l["auto_increment"]&&!$Xd)$K=lang(49);return
explode("/",$K);}function
editInput($R,array$l,$b,$Y){if($l["type"]=="enum")return(isset($_GET["select"])?"<label><input type='radio'$b value='orig' checked><i>".lang(11)."</i></label> ":"").enum_input("radio",$b,$l,$Y,"NULL");return"";}function
editHint($R,array$l,$Y){return"";}function
processInput(array$l,$Y,$q=""){if($q=="SQL")return$Y;$C=$l["field"];$K=q($Y);if(preg_match('~^(now|getdate|uuid)$~',$q))$K="$q()";elseif(preg_match('~^current_(date|timestamp)$~',$q))$K=$q;elseif(preg_match('~^([+-]|\|\|)$~',$q))$K=idf_escape($C)." $q $K";elseif(preg_match('~^[+-] interval$~',$q))$K=idf_escape($C)." $q ".(preg_match("~^(\\d+|'[0-9.: -]') [A-Z_]+\$~i",$Y)&&JUSH!="pgsql"?$Y:$K);elseif(preg_match('~^(addtime|subtime|concat)$~',$q))$K="$q(".idf_escape($C).", $K)";elseif(preg_match('~^(md5|sha1|password|encrypt)$~',$q))$K="$q($K)";return
unconvert_field($l,$K);}function
dumpOutput(){$K=array('text'=>lang(63),'file'=>lang(64));if(function_exists('gzencode'))$K['gz']='gzip';return$K;}function
dumpFormat(){return(support("dump")?array('sql'=>'SQL'):array())+array('csv'=>'CSV,','csv;'=>'CSV;','tsv'=>'TSV');}function
dumpPrint(){}function
dumpDatabase($i){}function
dumpTable($R,$lj,$Se=0){if($_POST["format"]!="sql"){echo"\xef\xbb\xbf";if($lj)dump_csv(array_keys(fields($R)));}else{if($Se==2){$m=array();foreach(fields($R)as$C=>$l)$m[]=idf_escape($C)." $l[full_type]";$g="CREATE TABLE ".table($R)." (".implode(", ",$m).")";}else$g=create_sql($R,$_POST["auto_increment"],$lj);set_utf8mb4($g);if($lj&&$g){if(($lj=="DROP+CREATE"&&!function_exists('Adminer\drop_sql'))||$Se==1)echo"DROP ".($Se==2?"VIEW":"TABLE")." IF EXISTS ".table($R).";\n";if($Se==1)$g=remove_definer($g);echo"$g;\n\n";}}}function
dumpData($R,$lj,$I,array$N=array(),array$Z=array(),array$Kd=array(),array$E=array()){if($lj){$Ff=(JUSH=="sqlite"?0:1048576);$m=array();$ne=false;if($_POST["format"]=="sql"){if($lj=="TRUNCATE+INSERT"&&!function_exists('Adminer\truncate_all_sql'))echo
truncate_sql($R).";\n";$m=fields($R);if(JUSH=="mssql"){foreach($m
as$l){if($l["auto_increment"]){echo"SET IDENTITY_INSERT ".table($R)." ON;\n";$ne=true;break;}}}}$J=($I!=""?connection()->query($I,1):driver()->select($R,($N?:array("*")),$Z,$Kd,$E,0));if($J){$Ee="";$Ra="";$Ye=array();$Fd=array();$nj="";$gd=($R!=''?'fetch_assoc':'fetch_row');$Db=0;while($L=$J->$gd()){if(!$Ye){$Kk=array();foreach($L
as$X){$l=$J->fetch_field();if(idx($m[$l->name],'generated')){$Fd[$l->name]=true;continue;}$Ye[]=$l->name;$w=idf_escape($l->name);$Kk[]="$w = VALUES($w)";}$nj=($lj=="INSERT+UPDATE"?"\nON DUPLICATE KEY UPDATE ".implode(", ",$Kk):"").";\n";}if($_POST["format"]!="sql"){if($lj=="table"){dump_csv($Ye);$lj="INSERT";}dump_csv($L);}else{if(!$Ee)$Ee="INSERT INTO ".table($R)." (".implode(", ",array_map('Adminer\idf_escape',$Ye)).") VALUES";foreach($L
as$w=>$X){if($Fd[$w]){unset($L[$w]);continue;}$l=$m[$w];$L[$w]=($X===null?"NULL":($X===false?0:unconvert_field($l,preg_match(number_type(),$l["type"])&&!preg_match('~\[~',$l["full_type"])&&is_numeric($X)?$X:(!is_blob($l)||is_utf8($X)?q($X):driver()->quoteBinary($X)))));}$xi=($Ff?"\n":" ")."(".implode(",\t",$L).")";if(!$Ra)$Ra=$Ee.$xi;elseif(JUSH=='mssql'?$Db%1000!=0:strlen($Ra)+4+strlen($xi)+strlen($nj)<$Ff)$Ra
.=",$xi";else{echo$Ra.$nj;$Ra=$Ee.$xi;}}$Db++;}if($Ra)echo$Ra.$nj;}elseif($_POST["format"]=="sql")echo"-- ".str_replace("\n"," ",connection()->error)."\n";if($ne)echo"SET IDENTITY_INSERT ".table($R)." OFF;\n";}}function
dumpFilename($me){return
friendly_url($me!=""?$me:(SERVER?:"localhost"));}function
dumpHeaders($me,$gg=false){$dh=$_POST["output"];$Yc=(preg_match('~sql~',$_POST["format"])?"sql":($gg?"tar":"csv"));header("Content-Type: ".($dh=="gz"?"application/x-gzip":($Yc=="tar"?"application/x-tar":($Yc=="sql"||$dh!="file"?"text/plain":"text/csv")."; charset=utf-8")));if($dh=="gz"){ob_start(function($Q){return
gzencode($Q);},1e6);}return$Yc;}function
dumpFooter(){if($_POST["format"]=="sql")echo"-- ".gmdate("Y-m-d H:i:s e")."\n";}function
importServerPath(){return"adminer.sql";}function
importPrint(){}function
importProcess(){return
false;}function
homepage(){echo'<p class="links">'.($_GET["ns"]==""&&support("database")?'<a href="'.h(ME).'database=">'.lang(65)."</a>\n":""),(support("scheme")?"<a href='".h(ME)."scheme='>".($_GET["ns"]!=""?lang(66):lang(67))."</a>\n":""),($_GET["ns"]!==""?'<a href="'.h(ME).'schema=">'.lang(68)."</a>\n":""),(support("privileges")?"<a href='".h(ME)."privileges='>".lang(69)."</a>\n":"");if($_GET["ns"]!=="")echo(support("routine")?"<a href='#routines'>".lang(70)."</a>\n":""),(support("sequence")?"<a href='#sequences'>".lang(71)."</a>\n":""),(support("type")?"<a href='#user-types'>".lang(7)."</a>\n":""),(support("event")?"<a href='#events'>".lang(72)."</a>\n":"");return
true;}function
navigation($bg){echo"<h1>".adminer()->name()." <span class='version'>".VERSION;$rg=$_COOKIE["adminer_version"];echo" <a href='https://www.adminer.org/#download'".target_blank()." id='version'>".(version_compare(VERSION,$rg)<0?h($rg):"").version_iframe()."</a>","</span></h1>\n";switch_lang();if($bg=="auth"){$dh="";foreach((array)$_SESSION["pwds"]as$Mk=>$Li){foreach($Li
as$O=>$Fk){$C=h(get_setting("vendor-$Mk-$O")?:get_driver($Mk));foreach($Fk
as$V=>$G){if($C&&$G!==null){$Tb=$_SESSION["db"][$Mk][$O][$V];foreach(($Tb?array_keys($Tb):array(""))as$i)$dh
.="<li><a href='".h(auth_url($Mk,$O,$V,$i))."'>($C) ".h("$V@").($O!=""?adminer()->serverName($O):"").h($i!=""?" - $i":"")."</a>\n";}}}}if($dh)echo"<ul id='logins'".on('mouseover','menuOver').on('mouseout','menuOut').">\n$dh</ul>\n";}else{$T=array();if($_GET["ns"]!==""&&!$bg&&DB!=""){connection()->select_db(DB);$T=table_status('',true);}adminer()->syntaxHighlighting($T);adminer()->databasesPrint($bg);$ia=array();if(DB==""||!$bg){if(support("sql")){$ia['sql']="<a href='".h(ME)."sql='".bold(isset($_GET["sql"])&&!isset($_GET["import"])).">".lang(62)."</a>";$ia['import']="<a href='".h(ME)."import='".bold(isset($_GET["import"])).">".lang(73)."</a>";}$ia['dump']="<a href='".h(ME)."dump=".url_escape(isset($_GET["table"])?$_GET["table"]:$_GET["select"])."' id='dump'".bold(isset($_GET["dump"])).">".lang(74)."</a>";}$se=$_GET["ns"]!==""&&!$bg&&DB!="";if($se&&function_exists('Adminer\alter_table'))$ia['create']='<a href="'.h(ME).'create="'.bold($_GET["create"]==="").">".lang(75)."</a>";$ia=adminer()->menuActions($ia,$bg);echo($ia?"<p class='links'>\n".implode("\n",$ia)."\n":"");if($se){if($T)adminer()->tablesPrint($T);else
echo"<p class='message'>".lang(12)."</p>\n";}}}function
syntaxHighlighting(array$T){echo
script_src(preg_replace("~\\?.*~","",ME)."?file=jush.js&version=6.0.1",true);$dg=preg_replace('~<(?=/script)~i','<\\',Driver::jushModule());echo($dg?script("addEventListener('DOMContentLoaded', () => {\n$dg\n});"):"");if(support("sql")){echo"<script".nonce().">\n";if($T){$sf=array();foreach($T
as$R=>$U)$sf[]=js_escape_re($R);echo"var jushLinks = { ".JUSH.":";json_row(js_escape(ME).(support("table")?"table":"select").'=$&','/\b(?<!\$)('.implode('|',$sf).')(?!\$)\b/g',false);$dj=array("sql","check","event","procedure","trigger","view","type","table","processlist");if(support("routine")&&array_intersect_key($_GET,array_flip($dj))){foreach(routines()as$L)json_row(js_escape(ME).'function='.url_escape($L["SPECIFIC_NAME"]).'&name=$&','/\b'.js_escape_re($L["ROUTINE_NAME"]).'(?=["`\]]?\()/g',false);}json_row('');echo"};\n";foreach(array("bac","bra","sqlite_quo","mssql_bra")as$X)echo"jushLinks.$X = jushLinks.".JUSH.";\n";if(isset($_GET["sql"])||isset($_GET["trigger"])||isset($_GET["check"])){$gj=(isset($_GET["trigger"])?array('INSERT INTO','UPDATE','DELETE FROM'):(isset($_GET["check"])?array():null));$Ca=Driver::jushAutocomplete($T,$gj);echo($Ca?"addEventListener('DOMContentLoaded', () => { autocompleter = $Ca; });\n":"");}}echo"</script>\n";}echo
script("syntaxHighlighting('".(preg_match('~^\d\.?\d~',connection()->server_info,$A)?$A[0]:"")."', '".connection()->flavor."');");}function
databasesPrint($bg){if(support("single_db"))return;$h=adminer()->databases();if(DB&&$h&&!in_array(DB,$h))array_unshift($h,DB);echo"<form action=''>\n<p id='dbs'>\n";hidden_fields_get();$Rb=on('mousedown','dbMouseDown').on('change','dbChange');echo"<label title='".lang(33)."'>".lang(76).": ".($h?html_select("db",array(""=>"")+$h,DB,$Rb):"<input name='db' value='".h(DB)."' autocapitalize='off' size='19'>\n")."</label>","<input type='submit' value='".lang(24)."'".($h?" class='hidden'":"").">\n";foreach(array("import","sql","schema","dump","privileges")as$X){if(isset($_GET[$X])){echo
input_hidden($X);break;}}echo"</p></form>\n";}function
menuActions(array$ia,$bg){return$ia;}function
tablesPrint(array$T){echo"<ul id='tables'".on('mouseover','menuOver').on('mouseout','menuOut').">";foreach($T
as$R=>$hj){$R="$R";$C=adminer()->tableName($hj);if($C!=""&&!$hj["partition"])echo'<li><a href="'.h(ME).'select='.url_escape($R).'"'.bold($_GET["select"]==$R||$_GET["edit"]==$R,"select hover")." title='".lang(39)."'>".lang(77)."</a> ",(support("table")||support("indexes")?'<a href="'.h(ME).'table='.url_escape($R).'"'.bold(in_array($R,array($_GET["table"],$_GET["create"],$_GET["indexes"],$_GET["foreign"],$_GET["trigger"],$_GET["check"],$_GET["view"])),(is_view($hj)?"view":"structure"))." title='".lang(40)."'>$C</a>":"<span>$C</span>")."\n";}echo"</ul>\n";}function
showVariables(){return
show_variables();}function
showStatus(){return
show_status();}function
processList(){return
process_list();}function
killProcess($s){return
kill_process($s);}}class
Plugins{private
static$append=array('dumpFormat'=>true,'dumpOutput'=>true,'editRowPrint'=>true,'editFunctions'=>true,'config'=>true);var$plugins;var$drivers=array();var$driverFiles=array();var$error='';private$hooks=array();function
__construct($Ch){$sc=SqlDriver::$drivers;$de=" href='https://www.adminer.org/plugins/#use'".target_blank();if($Ch===null){$Ch=array();$Ka="adminer-plugins";if(is_dir($Ka)){foreach(glob("$Ka/*.php")as$n){$ld=SqlDriver::$drivers;$this->includeOnce($n);foreach(array_diff_key(SqlDriver::$drivers,$ld)as$s=>$C)$this->driverFiles[$s]=$n;}}if(file_exists("$Ka.php")){$ue=$this->includeOnce("$Ka.php");if(is_array($ue)){foreach($ue
as$w=>$_h)$Ch[is_object($_h)?get_class($_h):$w]=$_h;}else$this->error
.=lang(78,"<b>$Ka.php</b>",$de)."<br>";}foreach(get_declared_classes()as$gb){if(!$Ch[$gb]&&(preg_match('~^Adminer\w~i',$gb)||is_subclass_of($gb,'Adminer\Plugin'))){$gi=new
\ReflectionClass($gb);$wb=$gi->getConstructor();if($wb&&$wb->getNumberOfRequiredParameters())$this->error
.=lang(79,$de,"<b>$gb</b>","<b>$Ka.php</b>")."<br>";else$Ch[$gb]=new$gb;}}}$Je=array_filter($Ch,function($_h){return!is_object($_h);});if($Je){$this->error
.=lang(80,$de)."<br>";$Ch=array_diff_key($Ch,$Je);}$this->drivers=array_diff_key(SqlDriver::$drivers,$sc);$this->plugins=$Ch;$ja=new
Adminer;$Ch[]=$ja;$gi=new
\ReflectionObject($ja);foreach($gi->getMethods()as$Yf){foreach($Ch
as$_h){$C=$Yf->getName();if(method_exists($_h,$C))$this->hooks[$C][]=$_h;}}}function
includeOnce($n){return
include_once"./$n";}static
function
checksum($n){$kd=str_replace("\r","",file_get_contents($n));$kd=preg_replace('~\n\tprotected \$translations = array\(.*?\n\t\);~s','',$kd);return
dechex(crc32($kd));}function
checksums(){$md=array_values($this->driverFiles);foreach($this->plugins
as$_h){$gi=new
\ReflectionObject($_h);$md[]=$gi->getFileName();}$K=array();foreach($md
as$n)$K[basename($n,'.php')]=self::checksum($n);return$K;}static
function
officialChecksums(){return
array('adminer.js'=>'a0599090','backward-keys'=>'ed1ef78f','before-unload'=>'2a613523','config'=>'722eb4af','dark-switcher'=>'3d490dea','database-hide'=>'e304a899','designs'=>'d1515f34','dump-alter'=>'896b579e','dump-bz2'=>'f0d0e336','dump-date'=>'adc7f1c7','dump-json'=>'767dd321','dump-xml'=>'4fc3cd60','dump-zip'=>'93817d96','edit-foreign'=>'72ad1562','edit-textarea'=>'a24c3cc','editor-setup'=>'a7dc3a37','editor-views'=>'5c12b185','enum-option'=>'96ee8718','file-upload'=>'10add0e8','foreign-system'=>'ebb4c654','frames'=>'b0e1d11a','highlight-codemirror'=>'f4baf411','highlight-monaco'=>'edd1b0af','highlight-prism'=>'267948e5','import-csv'=>'d429c77','login-ip'=>'4d174fea','login-otp'=>'5b5a68af','login-passkey'=>'f69f2f06','login-password-less'=>'e150daac','login-reverse-proxy'=>'24558ea2','login-servers'=>'19c42e45','login-ssl'=>'6ed147bc','login-table'=>'811f8cef','menu-links'=>'7f3d5020','remote-color'=>'86a39047','row-numbers'=>'eec8698c','select-email'=>'f84fbd2c','select-image'=>'f55c0231','slugify'=>'dec64713','sql-gemini'=>'c60ab309','sql-log'=>'8e435000','table-indexes-structure'=>'a90cc0c9','table-structure'=>'a8458e02','tables-filter'=>'ec2bcd6e','timeout'=>'97321caf','version-github'=>'627cadf9','version-noverify'=>'966937e9','clickhouse'=>'b0f6631c','elastic'=>'27503b8b','firebird'=>'5499d1a','igdb'=>'59055fd3','imap'=>'ac143217','mongo'=>'c3b8f5a4','redis'=>'ba56e72e','simpledb'=>'92f050ad',);}function
__call($C,array$hh){$va=array();foreach($hh
as$w=>$X)$va[]=&$hh[$w];$K=null;foreach($this->hooks[$C]as$_h){$Y=call_user_func_array(array($_h,$C),$va);if($Y!==null){if(!self::$append[$C])return$Y;$K=$Y+(array)$K;}}return$K;}}abstract
class
Plugin{protected$translations=array();function
description(){return$this->lang('');}function
screenshot(){return"";}protected
function
lang($t,$D=null){$va=func_get_args();$va[0]=idx($this->translations[LANG],$t)?:$t;return
call_user_func_array('Adminer\lang_format',$va);}}class
Password{private$password_hash;private$password_matches=null;function
__construct($sh){$this->password_hash=$sh;}function
description(){return
lang(81);}function
credentials(){$G=get_password();return
array(SERVER,$_GET["username"],($this->passwordMatches($G)&&!password_required()?"":$G));}function
login($wf,$G){if($this->passwordMatches($G))return
true;}protected
function
passwordMatches($G){if($this->password_matches===null)$this->password_matches=(function_exists('password_verify')&&password_verify(strval($G),$this->password_hash));return$this->password_matches;}}Adminer::$instance=(function_exists('adminer_object')?adminer_object():(is_dir("adminer-plugins")||file_exists("adminer-plugins.php")?new
Plugins(null):new
Adminer));SqlDriver::$drivers=array("server"=>"MySQL / MariaDB")+SqlDriver::$drivers;if(!defined('Adminer\DRIVER')){define('Adminer\DRIVER',"server");if(extension_loaded("mysqli")&&$_GET["ext"]!="pdo"){class
Db
extends
\mysqli{static$instance;var$extension="MySQLi",$flavor='';function
__construct(){parent::init();}function
attach($O,$V,$G){mysqli_report(MYSQLI_REPORT_OFF);list($ie,$Dh)=host_port($O);$ej=adminer()->connectSsl();$Dk=($ej&&($ej['key']||$ej['cert']||$ej['ca']||isset($ej['verify'])));if($Dk)$this->ssl_set($ej['key'],$ej['cert'],$ej['ca'],'','');$K=@$this->real_connect(($O!=""?$ie:ini_get("mysqli.default_host")),($O.$V!=""?$V:ini_get("mysqli.default_user")),($O.$V.$G!=""?$G:ini_get("mysqli.default_pw")),null,(is_numeric($Dh)?intval($Dh):ini_get("mysqli.default_port")),(is_numeric($Dh)?null:$Dh),($Dk?($ej['verify']!==false?MYSQLI_CLIENT_SSL:64):0));$this->options(MYSQLI_OPT_LOCAL_INFILE,0);return($K?'':$this->error);}function
set_charset($Xa){if(parent::set_charset($Xa))return
true;parent::set_charset('utf8');return$this->query("SET NAMES $Xa");}function
next_result(){return
self::more_results()&&parent::next_result();}function
quote($Q){return"'".$this->escape_string($Q)."'";}function
inTransaction(){return
false;}}}elseif(extension_loaded("mysql")&&!((ini_bool("sql.safe_mode")||ini_bool("mysql.allow_local_infile"))&&extension_loaded("pdo_mysql"))){class
Db
extends
SqlDb{private$link;function
attach($O,$V,$G){if(ini_bool("mysql.allow_local_infile"))return
lang(82,"'mysql.allow_local_infile'","MySQLi","PDO_MySQL");$this->link=@mysql_connect(($O!=""?$O:ini_get("mysql.default_host")),($O.$V!=""?$V:ini_get("mysql.default_user")),($O.$V.$G!=""?$G:ini_get("mysql.default_password")),true,131072);if(!$this->link)return
mysql_error();$this->server_info=mysql_get_server_info($this->link);return'';}function
set_charset($Xa){return
mysql_set_charset($Xa,$this->link)||mysql_set_charset('utf8',$this->link);}function
quote($Q){return"'".mysql_real_escape_string($Q,$this->link)."'";}function
select_db($Qb){return
mysql_select_db($Qb,$this->link);}function
query($I,$nk=false){$J=@($nk?mysql_unbuffered_query($I,$this->link):mysql_query($I,$this->link));$this->error="";if(!$J){$this->errno=mysql_errno($this->link);$this->error=mysql_error($this->link);return
false;}if($J===true){$this->affected_rows=mysql_affected_rows($this->link);$this->info=mysql_info($this->link);return
true;}return
new
Result($J);}}class
Result{var$num_rows;private$result;private$offset=0;function
__construct($J){$this->result=$J;$this->num_rows=mysql_num_rows($J);}function
fetch_assoc(){return
mysql_fetch_assoc($this->result);}function
fetch_row(){return
mysql_fetch_row($this->result);}function
fetch_field(){$K=mysql_fetch_field($this->result,$this->offset++);$K->orgtable=$K->table;$K->charsetnr=($K->blob?63:0);return$K;}}}elseif(extension_loaded("pdo_mysql")){class
Db
extends
PdoDb{var$extension="PDO_MySQL";function
attach($O,$V,$G){$Pg=array(\PDO::MYSQL_ATTR_LOCAL_INFILE=>false);if(isset($_GET["select"]))$Pg[\PDO::MYSQL_ATTR_MULTI_STATEMENTS]=false;$ej=adminer()->connectSsl();if($ej){if($ej['key'])$Pg[\PDO::MYSQL_ATTR_SSL_KEY]=$ej['key'];if($ej['cert'])$Pg[\PDO::MYSQL_ATTR_SSL_CERT]=$ej['cert'];if($ej['ca'])$Pg[\PDO::MYSQL_ATTR_SSL_CA]=$ej['ca'];if(isset($ej['verify']))$Pg[\PDO::MYSQL_ATTR_SSL_VERIFY_SERVER_CERT]=$ej['verify'];}list($ie,$Dh)=host_port($O);return$this->dsn("mysql:charset=utf8".($ie!=""?";host=$ie":'').($Dh?(is_numeric($Dh)?";port=":";unix_socket=").$Dh:""),$V,$G,$Pg);}function
set_charset($Xa){return$this->query("SET NAMES $Xa");}function
select_db($Qb){return$this->query("USE ".idf_escape($Qb));}function
query($I,$nk=false){$this->pdo->setAttribute(\PDO::MYSQL_ATTR_USE_BUFFERED_QUERY,!$nk);return
parent::query($I,$nk);}}}class
Driver
extends
SqlDriver{static$extensions=array("MySQLi","MySQL","PDO_MySQL");static$jush="sql";var$unsigned=array("unsigned","zerofill","unsigned zerofill");var$operators=array("=","<",">","<=",">=","!=","LIKE","LIKE %%","REGEXP","IN","FIND_IN_SET","IS NULL","NOT LIKE","NOT REGEXP","NOT IN","IS NOT NULL","SQL");var$functions=array("char_length","date","from_unixtime","lower","round","floor","ceil","sec_to_time","time_to_sec","upper");var$grouping=array("avg","count","count distinct","group_concat","max","min","sum");var$partitionBy=array("HASH","LINEAR HASH","KEY","LINEAR KEY","RANGE","LIST");static
function
connect($O,$V,$G){$e=parent::connect($O,$V,$G);if(is_string($e)){if(function_exists('iconv')&&!is_utf8($e)&&strlen($xi=iconv("windows-1252","utf-8//IGNORE",$e))>strlen($e))$e=$xi;return$e;}$e->set_charset(charset($e));$e->query("SET sql_quote_show_create = 1, autocommit = 1");$e->flavor=(preg_match('~MariaDB~',$e->server_info)?'maria':'mysql');add_driver(DRIVER,($e->flavor=='maria'?"MariaDB":"MySQL"));return$e;}function
__construct(Db$e){parent::__construct($e);$this->types=array(lang(83)=>array("tinyint"=>3,"smallint"=>5,"mediumint"=>8,"int"=>10,"bigint"=>20,"decimal"=>66,"float"=>12,"double"=>21),lang(84)=>array("date"=>10,"datetime"=>19,"timestamp"=>19,"time"=>10,"year"=>4),lang(85)=>array("char"=>255,"varchar"=>65535,"tinytext"=>255,"text"=>65535,"mediumtext"=>16777215,"longtext"=>4294967295),lang(86)=>array("enum"=>65535,"set"=>64),lang(87)=>array("bit"=>20,"binary"=>255,"varbinary"=>65535,"tinyblob"=>255,"blob"=>65535,"mediumblob"=>16777215,"longblob"=>4294967295),lang(88)=>array("geometry"=>0,"point"=>0,"linestring"=>0,"polygon"=>0,"multipoint"=>0,"multilinestring"=>0,"multipolygon"=>0,"geometrycollection"=>0),);$this->insertFunctions=array("char"=>"md5/sha1/password/encrypt/uuid","binary"=>"md5/sha1","date|time"=>"now",);$this->editFunctions=array(number_type()=>"+/-","date"=>"+ interval/- interval","time"=>"addtime/subtime","char|text"=>"concat",);if(min_version('5.7.8',10.2,$e))$this->types[lang(85)]["json"]=4294967295;if(min_version('',10.7,$e)){$this->types[lang(85)]["uuid"]=128;$this->insertFunctions['uuid']='uuid';}if(min_version('',10.5,$e)){$this->types[lang(89)]["inet6"]=39;if(min_version('','10.10',$e))$this->types[lang(89)]["inet4"]=15;}if(min_version(9,11.7,$e))$this->types[lang(83)]["vector"]=16383;if(min_version(5.7,10.2,$e))$this->generated=array("STORED","VIRTUAL");}function
unconvertFunction(array$l){return(preg_match("~binary~",$l["type"])?"<code class='jush-sql'>UNHEX</code>":($l["type"]=="bit"?doc_link(array('sql'=>'bit-value-literals.html'),"<code>b''</code>"):($l["type"]=="vector"?"<code class='jush-sql'>".($this->conn->flavor=='maria'?"VEC_FromText":"STRING_TO_VECTOR")."</code>":(preg_match("~geom|point|linestring|polygon~",$l["type"])?"<code class='jush-sql'>GeomFromText</code>":""))));}function
insert($R,array$P){return($P?parent::insert($R,$P):queries("INSERT INTO ".table($R)." ()\nVALUES ()"));}function
insertUpdate($R,array$M,array$Nh){$d=array_keys(reset($M));$Kh="INSERT INTO ".table($R)." (".implode(", ",$d).") VALUES\n";$Kk=array();foreach($d
as$w)$Kk[$w]="$w = VALUES($w)";$nj="\nON DUPLICATE KEY UPDATE ".implode(", ",$Kk);$Kk=array();$x=0;foreach($M
as$P){$Y="(".implode(", ",$P).")";if($Kk&&(strlen($Kh)+$x+strlen($Y)+strlen($nj)>1e6)){if(!queries($Kh.implode(",\n",$Kk).$nj))return
false;$Kk=array();$x=0;}$Kk[]=$Y;$x+=strlen($Y)+2;}return
queries($Kh.implode(",\n",$Kk).$nj);}function
slowQuery($I,$Pj){if(min_version('5.7.8','10.1.2')){if($this->conn->flavor=='maria')return"SET STATEMENT max_statement_time=$Pj FOR $I";elseif(preg_match('~^(SELECT\b)(.+)~is',$I,$A))return"$A[1] /*+ MAX_EXECUTION_TIME(".($Pj*1000).") */ $A[2]";}}function
convertColumn($t,array$l){if(preg_match("~binary~",$l["type"]))return"HEX($t)";if($l["type"]=="bit")return"BIN($t + 0)";if($l["type"]=="vector")return($this->conn->flavor=='maria'?"VEC_ToText":"VECTOR_TO_STRING")."($t)";if(preg_match("~geom|point|linestring|polygon~",$l["type"]))return(min_version(8)?"ST_":"")."AsWKT($t)";return"";}function
convertSearch($t,array$X,array$l){return($this->convertColumn($t,$l)?:(preg_match('~'.text_type().'~',$l["type"])&&!preg_match("~^utf8~",$l["collation"])&&preg_match('~[\x80-\xFF]~',$X['val'])?"CONVERT($t USING ".charset($this->conn).")":$t));}function
typeName(\stdClass$l){$mk=array("decimal","tinyint","smallint","int","float","double",7=>"timestamp","bigint","mediumint","date","time","datetime","year",15=>"varchar","bit",242=>"vector",245=>"json","decimal","enum","set","tinytext","mediumtext","longtext","text","varchar","char","geometry",);$K=idx($mk,$l->type,"");return
parent::typeName($l)?:($l->charsetnr==63?str_replace(array("text","varchar","char"),array("blob","varbinary","binary"),$K):$K);}function
quoteBinary($xi){return"X".q(bin2hex($xi));}function
warnings(){$J=$this->conn->query("SHOW WARNINGS");if($J&&$J->num_rows){ob_start();print_select_result($J);return
ob_get_clean();}}function
tableHelp($C,$Se=false){$yf=($this->conn->flavor=='maria');if(information_schema(DB))return
strtolower(str_replace("_","-",DB)."-".($yf?"$C-table/":str_replace("_","-",$C)."-table.html"));if(DB=="sys")return($yf?"sys-schema/":strtolower("sys-".str_replace("_","-",preg_replace('~^x\$~','',$C)).".html"));if(DB=="mysql")return($yf?"mysql$C-table/":"system-schema.html");}function
partitionsInfo($R){$Ad="FROM information_schema.PARTITIONS WHERE TABLE_SCHEMA = ".q(DB)." AND TABLE_NAME = ".q($R);$J=$this->conn->query("SELECT PARTITION_METHOD, PARTITION_EXPRESSION, PARTITION_ORDINAL_POSITION $Ad ORDER BY PARTITION_ORDINAL_POSITION DESC LIMIT 1");$L=($J?$J->fetch_row():null);if(!$L)return
array();$K=array();list($K["partition_by"],$K["partition"],$K["partitions"])=$L;$ph=get_key_vals("SELECT PARTITION_NAME, PARTITION_DESCRIPTION $Ad AND PARTITION_NAME != '' ORDER BY PARTITION_ORDINAL_POSITION");$K["partition_names"]=array_keys($ph);$K["partition_values"]=array_values($ph);return$K;}function
hasCStyleEscapes(){static$Sa;if($Sa===null){$cj=get_val("SHOW VARIABLES LIKE 'sql_mode'",1,$this->conn);$Sa=(strpos($cj,'NO_BACKSLASH_ESCAPES')===false);}return$Sa;}function
lineComment(){return"#|-- ";}function
engines(){$K=array();foreach(get_rows("SHOW ENGINES")as$L){if(preg_match("~YES|DEFAULT~",$L["Support"]))$K[]=$L["Engine"];}return$K;}function
indexAlgorithms(array$tj){return(preg_match('~^(MEMORY|NDB)$~',$tj["Engine"])?array("HASH","BTREE"):array());}}function
idf_escape($t){return"`".str_replace("`","``",$t)."`";}function
table($t){return
idf_escape($t);}function
get_databases($td){$K=get_session("dbs");if($K===null){$I="SELECT SCHEMA_NAME FROM information_schema.SCHEMATA ORDER BY SCHEMA_NAME";$fj=microtime(true);$K=($td?slow_query($I):get_vals($I));if(microtime(true)-$fj>0.1){restart_session();set_session("dbs",$K);stop_session();}}return$K;}function
limit($I,$Z,$y,$Ag=0,$Ji=" "){return" $I$Z".($y?$Ji."LIMIT $y".($Ag?" OFFSET $Ag":""):"");}function
limit1($R,$I,$Z,$Ji="\n"){return
limit($I,$Z,1,0,$Ji);}function
db_collation($i,array$lb){$K=null;$g=get_val("SHOW CREATE DATABASE ".idf_escape($i),1);if(preg_match('~ COLLATE ([^ ]+)~',$g,$A))$K=$A[1];elseif(preg_match('~ CHARACTER SET ([^ ]+)~',$g,$A))$K=$lb[$A[1]][-1];return$K;}function
logged_user(){return
get_val("SELECT USER()");}function
tables_list(){return
get_key_vals("SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME");}function
count_tables(array$h){$K=array();foreach($h
as$i)$K[$i]=count(get_vals("SHOW TABLES IN ".idf_escape($i)));return$K;}function
table_status($C="",$ed=false){$K=array();foreach(get_rows($ed?"SELECT TABLE_NAME AS Name, ENGINE AS Engine, TABLE_COMMENT AS Comment FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() ".($C!=""?"AND TABLE_NAME = ".q($C):"ORDER BY Name"):"SHOW TABLE STATUS".($C!=""?" LIKE ".q(addcslashes($C,"%_\\")):""))as$L){if($L["Engine"]=="InnoDB")$L["Comment"]=preg_replace('~(?:(.+); )?InnoDB free: .*~','\1',$L["Comment"]);if(!isset($L["Engine"]))$L["Comment"]="";if($C!="")$L["Name"]=$C;$K[$L["Name"]]=$L;}return$K;}function
is_view(array$S){return$S["Engine"]===null;}function
fk_support(array$S){return
preg_match('~InnoDB|IBMDB2I'.(min_version(5.6)?'|NDB':'').'~i',$S["Engine"]);}function
parse_type($Cd){preg_match('~^([^( ]+)(?:\((.+)\))?( unsigned)?( zerofill)?$~',$Cd,$A);return
array($A[1],$A[2],ltrim($A[3].$A[4]));}function
fields($R){$yf=(connection()->flavor=='maria');$K=array();foreach(get_rows("SELECT * FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ".q($R)." ORDER BY ORDINAL_POSITION")as$L){$l=$L["COLUMN_NAME"];$U=$L["COLUMN_TYPE"];$Gd=$L["GENERATION_EXPRESSION"];$bd=$L["EXTRA"];preg_match('~^(VIRTUAL|PERSISTENT|STORED)~',$bd,$Fd);list($lk,$x,$tk)=parse_type($U);$j=$L["COLUMN_DEFAULT"];if($j!=""){$Re=preg_match('~text|json~',$lk);if(!$yf&&$Re)$j=preg_replace("~^(_\w+)?('.*')$~",'\2',stripslashes($j));if($yf||$Re){$j=($j=="NULL"?null:preg_replace_callback("~^'(.*)'$~",function($A){return
stripslashes(str_replace("''","'",$A[1]));},$j));}if(!$yf&&preg_match('~binary~',$lk)&&preg_match('~^0x(\w*)$~',$j,$A))$j=pack("H*",$A[1]);}$K[$l]=array("field"=>$l,"full_type"=>$U,"type"=>$lk,"length"=>$x,"unsigned"=>$tk,"default"=>($Fd?($yf?$Gd:stripslashes($Gd)):$j),"null"=>($L["IS_NULLABLE"]=="YES"),"auto_increment"=>($bd=="auto_increment"),"on_update"=>(preg_match('~\bon update (\w+)~i',$bd,$A)?$A[1]:""),"collation"=>$L["COLLATION_NAME"],"privileges"=>array_flip(explode(",","$L[PRIVILEGES],where,order")),"comment"=>$L["COLUMN_COMMENT"],"primary"=>($L["COLUMN_KEY"]=="PRI"),"generated"=>($Fd[1]=="PERSISTENT"?"STORED":$Fd[1]),);}return$K;}function
indexes($R,$f=null){$K=array();foreach(get_rows("SHOW INDEX FROM ".table($R),$f)as$L){$C=$L["Key_name"];$K[$C]["type"]=($C=="PRIMARY"?"PRIMARY":($L["Index_type"]=="FULLTEXT"?"FULLTEXT":($L["Non_unique"]?(preg_match('~^(SPATIAL|VECTOR)$~',$L["Index_type"])?$L["Index_type"]:"INDEX"):"UNIQUE")));$K[$C]["columns"][]=$L["Column_name"];$K[$C]["lengths"][]=($L["Index_type"]=="SPATIAL"?null:$L["Sub_part"]);$K[$C]["descs"][]=null;$K[$C]["algorithm"]=$L["Index_type"];}return$K;}function
foreign_keys($R){static$wh='(?:`(?:[^`]|``)+`|"(?:[^"]|"")+")';$K=array();$Eb=get_val("SHOW CREATE TABLE ".table($R),1);if($Eb){preg_match_all("~CONSTRAINT ($wh) FOREIGN KEY ?\\(((?:$wh,? ?)+)\\) REFERENCES ($wh)(?:\\.($wh))? \\(((?:$wh,? ?)+)\\)(?: ON DELETE (".driver()->onActions."))?(?: ON UPDATE (".driver()->onActions."))?~",$Eb,$_f,PREG_SET_ORDER);foreach($_f
as$A){preg_match_all("~$wh~",$A[2],$Wi);preg_match_all("~$wh~",$A[5],$Fj);$K[idf_unescape($A[1])]=array("db"=>idf_unescape($A[4]!=""?$A[3]:$A[4]),"table"=>idf_unescape($A[4]!=""?$A[4]:$A[3]),"source"=>array_map('Adminer\idf_unescape',$Wi[0]),"target"=>array_map('Adminer\idf_unescape',$Fj[0]),"on_delete"=>($A[6]?:"RESTRICT"),"on_update"=>($A[7]?:"RESTRICT"),);}}return$K;}function
view($C){return
array("select"=>preg_replace('~^(?:[^`]|`[^`]*`)*\s+AS\s+~isU','',get_val("SHOW CREATE VIEW ".table($C),1)));}function
collations(){$K=array();foreach(get_rows("SHOW COLLATION")as$L){if($L["Default"])$K[$L["Charset"]][-1]=$L["Collation"];else$K[$L["Charset"]][]=$L["Collation"];}ksort($K);foreach($K
as$w=>$X)sort($K[$w]);return$K;}function
information_schema($i,$zi=""){return($i=="information_schema")||(min_version(5.5)&&$i=="performance_schema");}function
error(){return
h(preg_replace('~^You have an error.*syntax to use~U',"Syntax error",connection()->error));}function
create_database($i,$kb){return
queries("CREATE DATABASE ".idf_escape($i).($kb?" COLLATE ".q($kb):""));}function
drop_databases(array$h){$K=apply_queries("DROP DATABASE",$h,'Adminer\idf_escape');restart_session();set_session("dbs",null);return$K;}function
rename_database($C,$kb){$K=false;if(create_database($C,$kb)){$T=array();$Pk=array();foreach(tables_list()as$R=>$U){if($U=='VIEW')$Pk[]=$R;else$T[]=$R;}$K=(!$T&&!$Pk)||move_tables($T,$Pk,$C);drop_databases($K?array(DB):array());}return$K;}function
auto_increment(){$Ba=" PRIMARY KEY";if($_GET["create"]!=""&&$_POST["auto_increment_col"]){foreach(indexes($_GET["create"])as$u){if(in_array($_POST["fields"][$_POST["auto_increment_col"]]["orig"],$u["columns"],true)){$Ba="";break;}if($u["type"]=="PRIMARY")$Ba=" UNIQUE";}}return" AUTO_INCREMENT$Ba";}function
alter_table($R,$C,array$m,array$vd,$pb,$Ec,$kb,$Aa,$oh){$ra=array();foreach($m
as$l){if($l[1]){$j=$l[1][3];if(preg_match('~ GENERATED~',$j)){$l[1][3]=(connection()->flavor=='maria'?"":$l[1][2]);$l[1][2]=$j;}$ra[]=($R!=""?($l[0]!=""?"CHANGE ".idf_escape($l[0]):"ADD"):" ")." ".implode($l[1]).($R!=""?$l[2]:"");}else$ra[]="DROP ".idf_escape($l[0]);}$ra=array_merge($ra,$vd);$hj=($pb!==null?" COMMENT=".q($pb):"").($Ec?" ENGINE=".q($Ec):"").($kb?" COLLATE ".q($kb):"").($Aa!=""?" AUTO_INCREMENT=$Aa":"");if($oh){$ph=array();if($oh["partition_by"]=='RANGE'||$oh["partition_by"]=='LIST'){foreach($oh["partition_names"]as$w=>$X){$Y=$oh["partition_values"][$w];$ph[]="\n  PARTITION ".idf_escape($X)." VALUES ".($oh["partition_by"]=='RANGE'?"LESS THAN":"IN").($Y!=""?" ($Y)":" MAXVALUE");}}$hj
.="\nPARTITION BY $oh[partition_by]($oh[partition])";if($ph)$hj
.=" (".implode(",",$ph)."\n)";elseif($oh["partitions"])$hj
.=" PARTITIONS ".(+$oh["partitions"]);}elseif($oh===null)$hj
.="\nREMOVE PARTITIONING";if($R=="")return
queries("CREATE TABLE ".table($C)." (\n".implode(",\n",$ra)."\n)$hj");if($R!=$C)$ra[]="RENAME TO ".table($C);if($hj)$ra[]=ltrim($hj);return($ra?queries("ALTER TABLE ".table($R)."\n".implode(",\n",$ra)):true);}function
alter_indexes($R,$ra){$Va=array();foreach($ra
as$X)$Va[]=($X[2]=="DROP"?"\nDROP INDEX ".idf_escape($X[1]):"\nADD $X[0] ".($X[0]=="PRIMARY"?"KEY ":"").($X[1]!=""?idf_escape($X[1])." ":"")."(".implode(", ",$X[2]).")");return
queries("ALTER TABLE ".table($R).implode(",",$Va));}function
truncate_tables(array$T){return
apply_queries("TRUNCATE TABLE",$T);}function
drop_views(array$Pk){return
queries("DROP VIEW ".implode(", ",array_map('Adminer\table',$Pk)));}function
drop_tables(array$T){return
queries("DROP TABLE ".implode(", ",array_map('Adminer\table',$T)));}function
move_tables(array$T,array$Pk,$Fj){$ki=array();foreach($T
as$R)$ki[]=table($R)." TO ".idf_escape($Fj).".".table($R);if(!$ki||queries("RENAME TABLE ".implode(", ",$ki))){$Zb=array();foreach($Pk
as$R)$Zb[table($R)]=view($R);connection()->select_db($Fj);$i=idf_escape(DB);foreach($Zb
as$C=>$Ok){if(!queries("CREATE VIEW $C AS ".str_replace(" $i."," ",$Ok["select"]))||!queries("DROP VIEW $i.$C"))return
false;}return
true;}return
false;}function
copy_tables(array$T,array$Pk,$Fj){queries("SET sql_mode = 'NO_AUTO_VALUE_ON_ZERO'");foreach($T
as$R){$C=($Fj==DB?table("copy_$R"):idf_escape($Fj).".".table($R));if(($_POST["overwrite"]&&!queries("\nDROP TABLE IF EXISTS $C"))||!queries("CREATE TABLE $C LIKE ".table($R))||!queries("INSERT INTO $C SELECT * FROM ".table($R)))return
false;foreach(get_rows("SHOW TRIGGERS LIKE ".q(addcslashes($R,"%_\\")))as$L){$ek=$L["Trigger"];list($Nc,$xg)=trigger_event($L);if(!queries("CREATE TRIGGER ".($Fj==DB?idf_escape("copy_$ek"):idf_escape($Fj).".".idf_escape($ek))." $L[Timing] $Nc".($xg!=""?" $xg":"")." ON $C FOR EACH ROW\n$L[Statement];"))return
false;}}foreach($Pk
as$R){$C=($Fj==DB?table("copy_$R"):idf_escape($Fj).".".table($R));$Ok=view($R);if(($_POST["overwrite"]&&!queries("DROP VIEW IF EXISTS $C"))||!queries("CREATE VIEW $C AS $Ok[select]"))return
false;}return
true;}function
trigger_event(array$L){$Pc=explode(",",$L["Event"]);$K=array();foreach(array("DELETE","INSERT","UPDATE")as$Nc){if(in_array($Nc,$Pc))$K[]=$Nc;}$K=implode(" OR ",$K);if(in_array("UPDATE",$Pc)&&min_version('','12.0.1')&&preg_match('~\s(?:BEFORE|AFTER)\s+(.+?)\s+ON\s~is',get_val("SHOW CREATE TRIGGER ".idf_escape($L["Trigger"]),2),$A)&&preg_match('~\bOF\s+(.+)~is',$A[1],$xg))return
array("$K OF",$xg[1]);return
array($K,"");}function
trigger($C,$R){if($C=="")return
array();$M=get_rows("SHOW TRIGGERS WHERE `Trigger` = ".q($C));$K=reset($M);if($K)list($K["Event"],$K["Of"])=trigger_event($K);return$K;}function
triggers($R){$K=array();foreach(get_rows("SHOW TRIGGERS LIKE ".q(addcslashes($R,"%_\\")))as$L){list($Nc)=trigger_event($L);$K[$L["Trigger"]]=array($L["Timing"],$Nc);}return$K;}function
trigger_options(){return
array("Timing"=>array("BEFORE","AFTER"),"Event"=>(min_version('','12.0.1')?array("INSERT","UPDATE","UPDATE OF","DELETE","INSERT OR UPDATE","INSERT OR UPDATE OF","DELETE OR INSERT","DELETE OR UPDATE","DELETE OR UPDATE OF","DELETE OR INSERT OR UPDATE","DELETE OR INSERT OR UPDATE OF",):array("INSERT","UPDATE","DELETE")),"Type"=>array("FOR EACH ROW"),);}function
routine($C,$U){$M=get_rows("SELECT PARAMETER_NAME, DTD_IDENTIFIER, PARAMETER_MODE, COLLATION_NAME
FROM information_schema.PARAMETERS
WHERE SPECIFIC_SCHEMA = DATABASE() AND ROUTINE_TYPE = '$U' AND SPECIFIC_NAME = ".q($C)."
ORDER BY ORDINAL_POSITION");$m=array();foreach($M
as$L){$Cd=$L["DTD_IDENTIFIER"];list($lk,$x,$tk)=parse_type($Cd);$m[]=array("field"=>$L["PARAMETER_NAME"],"type"=>$lk,"length"=>$x,"unsigned"=>$tk,"null"=>true,"full_type"=>$Cd,"inout"=>($U=="FUNCTION"?"":$L["PARAMETER_MODE"]),"collation"=>$L["COLLATION_NAME"],);}$K=connection()->query("SELECT
	ROUTINE_COMMENT comment,
	CONCAT(IF(IS_DETERMINISTIC = 'YES', 'DETERMINISTIC\\n', ''), IF(SQL_DATA_ACCESS != 'CONTAINS SQL', CONCAT(SQL_DATA_ACCESS, '\\n'), ''), ROUTINE_DEFINITION) definition,
	'SQL' language
FROM information_schema.ROUTINES
WHERE ROUTINE_SCHEMA = DATABASE() AND ROUTINE_TYPE = '$U' AND ROUTINE_NAME = ".q($C))->fetch_assoc();if($m&&$m[0]['field']=='')$K['returns']=array_shift($m);$K['fields']=$m;return$K;}function
routines(){return
get_rows("SELECT SPECIFIC_NAME, ROUTINE_NAME, ROUTINE_TYPE, DTD_IDENTIFIER FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE()");}function
routine_languages(){return
array();}function
routine_id($C,array$L){return
idf_escape($C);}function
last_id($J){return
get_val("SELECT LAST_INSERT_ID()");}function
explain(Db$e,$I){return$e->query("EXPLAIN ".(min_version(5.7)?"":"PARTITIONS ").$I);}function
found_rows(array$S,array$Z){return($Z||$S["Engine"]!="InnoDB"?null:$S["Rows"]);}function
create_sql($R,$Aa,$lj){$K=get_val("SHOW CREATE TABLE ".table($R),1);if(!$Aa)$K=preg_replace('~(\n\)[^\n]*?) AUTO_INCREMENT=\d+~','\1',$K);return$K;}function
truncate_sql($R){return"TRUNCATE ".table($R);}function
use_sql($Qb,$lj=""){$C=idf_escape($Qb);$K="";if(preg_match('~CREATE~',$lj)&&($g=get_val("SHOW CREATE DATABASE $C",1))){set_utf8mb4($g);if($lj=="DROP+CREATE")$K="DROP DATABASE IF EXISTS $C;\n";$K
.="$g;\n";}return$K."USE $C";}function
trigger_sql($R){$K="";foreach(get_rows("SHOW TRIGGERS LIKE ".q(addcslashes($R,"%_\\")),null,"-- ")as$L){list($L["Event"],$L["Of"])=trigger_event($L);$K
.="\n".create_trigger(" ON ".table($L["Table"]),$L+array("Type"=>"FOR EACH ROW")).";\n";}return$K;}function
show_variables(){return
get_rows("SHOW VARIABLES");}function
show_status(){return
get_rows("SHOW STATUS");}function
process_list(){return
get_rows("SHOW FULL PROCESSLIST");}function
convert_field(array$l){return
driver()->convertColumn(idf_escape($l["field"]),$l);}function
unconvert_field(array$l,$K){if(preg_match("~binary~",$l["type"]))$K="UNHEX($K)";if($l["type"]=="bit")$K="CONVERT(b$K, UNSIGNED)";if($l["type"]=="vector")$K=(connection()->flavor=='maria'?"VEC_FromText":"STRING_TO_VECTOR")."($K)";if(preg_match("~geom|point|linestring|polygon~",$l["type"])){$Kh=(min_version(8)?"ST_":"");$K=$Kh."GeomFromText($K, $Kh"."SRID($l[field]))";}return$K;}function
support($fd){return
preg_match('~^(comment|columns|copy|database|drop_col|dump|event|indexes|kill|privileges|move_col|procedure|processlist|routine|sql|status|table|trigger|variables|view'.(min_version(8)?'|descidx':'').(min_version('8.0.16','10.2.1')?'|check':'').(min_version(8,99)?'|fast_status':'').')$~',$fd);}function
kill_process($s){return
queries("KILL ".number($s));}function
connection_id(){return"SELECT CONNECTION_ID()";}function
max_connections(){return
get_val("SELECT @@max_connections");}function
types($ad=false){return
array();}function
type_values($s){return"";}function
type_definition($s){return
array("kind"=>"","definition"=>"");}function
schemas(){return
array();}function
get_schema(){return"";}function
set_schema($zi,$f=null){return
true;}}define('Adminer\JUSH',Driver::$jush);define('Adminer\SERVER',"".$_GET[DRIVER]);define('Adminer\DB',"$_GET[db]");define('Adminer\ME',preg_replace('~\?.*~','',relative_uri()).'?'.(sid()?SID.'&':'').($_GET["ext"]?"ext=".url_escape($_GET["ext"]).'&':'').(isset($_GET[DRIVER])?DRIVER."=".url_escape(SERVER).'&':'').(isset($_GET["username"])?"username=".url_escape($_GET["username"]).'&':'').(isset($_GET["db"])?'db='.url_escape(DB).'&'.(isset($_GET["ns"])?"ns=".url_escape($_GET["ns"])."&":""):''));function
page_header($Rj,$k="",$Qa=array(),$Sj=""){page_headers();if(is_ajax()&&$k){page_messages($k);exit;}if(!ob_get_level())ob_start('ob_gzhandler',4096);$Tj=$Rj.($Sj!=""?": $Sj":"");$Uj=strip_tags($Tj.(SERVER!=""&&SERVER!="localhost"?h(" - ".SERVER):"")." - ".adminer()->name());echo'<!DOCTYPE html>
<html lang=\'',LANG,'\' dir=\'',lang(90),'\' class=\'',lang(90),' nojs\'>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<meta name="robots" content="noindex">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>',$Uj,'</title>
<link rel="stylesheet" href="',h(preg_replace("~\\?.*~","",ME)."?file=default.css&version=6.0.1"),'">
';$Ib=adminer()->css();if(is_int(key($Ib)))$Ib=array_fill_keys($Ib,'light');$Vd=in_array('light',$Ib)||in_array('',$Ib);$Td=in_array('dark',$Ib)||in_array('',$Ib);$Mb=($Vd?($Td?null:false):($Td?:null));$Of=" media='(prefers-color-scheme: dark)'";if($Mb!==false)echo"<link rel='stylesheet'".($Mb?"":$Of)." href='".h(preg_replace("~\\?.*~","",ME)."?file=dark.css&version=6.0.1")."'>\n";echo"<meta name='color-scheme' content='".($Mb===null?"light dark":($Mb?"dark":"light"))."'>\n",script_src(preg_replace("~\\?.*~","",ME)."?file=functions.js&version=6.0.1");if(adminer()->head($Mb))echo"<link rel='icon' href='data:image/gif;base64,"."R0lGODlhEAAQAJEAAAQCBPz+/PwCBAROZCH5BAEAAAAALAAAAAAQABAAAAI2hI+pGO1rmghihiUdvUBnZ3XBQA7f05mOak1RWXrNq5nQWHMKvuoJ37BhVEEfYxQzHjWQ5qIAADs='>\n","<link rel='apple-touch-icon' href='".h(preg_replace("~\\?.*~","",ME)."?file=logo.png&version=6.0.1")."'>\n";foreach($Ib
as$zk=>$cg){$b=($cg=='dark'&&!$Mb?$Of:($cg=='light'&&$Td?" media='(prefers-color-scheme: light)'":""));echo"<link rel='stylesheet'$b href='".h($zk)."'>\n";}echo"\n<body class='";adminer()->bodyClass();echo"'>\n",script((isset($_COOKIE["adminer_version"])||!adminer()->verifyVersion()?"":"onload = partial(verifyVersion, '".VERSION."');\n")."
const offlineMessage = '".js_escape(lang(91))."';
const thousandsSeparator = '".js_escape(lang(5))."';
const urlSeparators = '".js_escape(ini_get("arg_separator.input"))."';"),"<div id='help' class='jush-".JUSH." jsonly hidden'".on('mouseover','helpKeep').on('mouseout','helpMouseout')."></div>\n","<div id='content'>\n","<span id='menuopen' class='jsonly'".on('click','menuToggle')."><button title='".lang(92)."' class='icon icon-move' aria-expanded='false'></button></span>\n";if($Qa!==null){$z=substr(preg_replace('~\b(username|db|ns)=[^&]*&~','',ME),0,-1);echo'<p id="breadcrumb"><a href="'.h($z?:".").'">'.get_driver(DRIVER).'</a> » ';$z=substr(preg_replace('~\b(db|ns)=[^&]*&~','',ME),0,-1);$O=adminer()->serverName(SERVER);$O=($O!=""?$O:lang(29));if($Qa===false)echo"$O\n";else{echo"<a href='".h($z.(DB!=""&&support("single_db")?"&db=":""))."' accesskey='1' title='Alt+Shift+1'>$O</a> » ";if($_GET["ns"]!=""||(DB!=""&&is_array($Qa)))echo'<a href="'.h($z."&db=".url_escape(DB).(support("scheme")?"&ns=":"").(support("single_table")?"&select=":"")).'">'.h(DB).'</a> » ';if(is_array($Qa)){if($_GET["ns"]!="")echo'<a href="'.h(substr(ME,0,-1)).'">'.h($_GET["ns"]).'</a> » ';foreach($Qa
as$w=>$X){$bc=(is_array($X)?$X[1]:h($X));if($bc!="")echo"<a href='".h(ME."$w=").url_escape(is_array($X)?$X[0]:$X)."'>$bc</a> » ";}}echo"$Rj\n";}}echo"<h2>$Tj</h2>\n","<div id='ajaxstatus' role='status' class='jsonly'></div>\n";restart_session();page_messages($k);$h=&get_session("dbs");if(DB!=""&&$h&&!in_array(DB,$h,true))$h=null;stop_session();define('Adminer\PAGE_HEADER',1);ob_flush();flush();}function
page_headers(){header("Content-Type: text/html; charset=utf-8");header("Cache-Control: no-cache");header("X-Frame-Options: deny");header("X-XSS-Protection: 0");header("X-Content-Type-Options: nosniff");header("Referrer-Policy: origin-when-cross-origin");foreach(adminer()->csp(csp())as$Hb){$Zd=array();foreach($Hb
as$w=>$X)$Zd[]="$w $X";header("Content-Security-Policy: ".implode("; ",$Zd));}adminer()->headers();}function
csp(){return
array(array("script-src"=>"'self' 'unsafe-inline' 'nonce-".get_nonce()."' 'strict-dynamic'","connect-src"=>"'self' https://www.adminer.org","frame-src"=>"https://www.adminer.org","object-src"=>"'none'","base-uri"=>"'none'","form-action"=>"'self'",),);}function
design_checksums(){$Ek=array();foreach(array_keys(adminer()->css())as$zk)$Ek[preg_replace('~\?.*~','',$zk)]=true;$K=array();foreach(array("adminer.css","adminer-dark.css")as$n){if($Ek[$n]&&file_exists($n)){preg_match('~^/\* Adminer design ([-\w]+) \*/~',file_get_contents($n),$A);$K[$n]=array((string)$A[1],Plugins::checksum($n));}}return$K;}function
official_design_checksums(){return
array('adminer-border/adminer-dark.css'=>'b2527e3','adminer-border/adminer.css'=>'430977ad','adminer-dark/adminer-dark.css'=>'a26bcd7b','brade/adminer.css'=>'be4161f0','bueltge/adminer.css'=>'1a8f00b4','dracula/adminer-dark.css'=>'cfaf61dd','esterka/adminer.css'=>'1f805f36','flat/adminer.css'=>'49a61af9','galkaev/adminer-dark.css'=>'16c46f94','haeckel/adminer.css'=>'147a3565','hever/adminer.css'=>'1f626deb','konya/adminer.css'=>'2b409696','lavender-light/adminer.css'=>'bf03f5d7','lucas-sandery/adminer.css'=>'6596353','mancave/adminer-dark.css'=>'e1ac813d','mvt/adminer.css'=>'ebd3afdc','nette/adminer.css'=>'5ab360e7','ng9/adminer.css'=>'488583cf','nicu/adminer.css'=>'ecb9bd1e','pappu687/adminer.css'=>'b58d128c','paranoiq/adminer.css'=>'64d27e5','pepa-linha/adminer.css'=>'baf25f0','pokorny/adminer.css'=>'ee9eea6d','price/adminer.css'=>'81be9a85','rmsoft/adminer.css'=>'6cd4a237','rmsoft_blue-dark/adminer.css'=>'32102a8','rmsoft_blue/adminer.css'=>'7d8d5b18','win98/adminer.css'=>'e82d63c3',);}function
version_iframe(){return(isset($_COOKIE["adminer_version"])||!adminer()->verifyVersion()?"":"<noscript><iframe sandbox src='https://www.adminer.org/version/?current=".VERSION."&amp;noscript=1'></iframe></noscript>");}function
get_nonce(){static$tg;if(!$tg)$tg=base64_encode(rand_string());return$tg;}function
page_messages($k){$yk=preg_replace('~^[^?]*~','',$_SERVER["REQUEST_URI"]);$Uf=idx($_SESSION["messages"],$yk);if($Uf){echo"<div class='message'>".implode("</div>\n<div class='message'>",$Uf)."</div>".script("messagesPrint();");unset($_SESSION["messages"][$yk]);}if($k)echo"<div class='error'>$k</div>\n";if(adminer()->error)echo"<div class='error'>".adminer()->error."</div>\n";}function
page_footer($bg=""){echo"</div>\n\n<div id='foot' class='foot'>\n<div id='menu'>\n";adminer()->navigation($bg);echo"</div>\n";if($bg!="auth")echo'<form action="" method="post">
<p class="logout">
<span title="',lang(31),'">',h($_GET["username"])."\n",'</span>
<input type=\'submit\' name=\'logout\' value=\'',lang(93),'\' id=\'logout\'>
',input_token(),'</form>
';echo"</div>\n\n",script("setupSubmitHighlight(document);");}function
int32($ig){while($ig>=2147483648)$ig-=4294967296;while($ig<=-2147483649)$ig+=4294967296;return(int)$ig;}function
long2str(array$W,$Rk){$xi='';foreach($W
as$X)$xi
.=pack('V',$X);if($Rk)return
substr($xi,0,end($W));return$xi;}function
str2long($xi,$Rk){$W=array_values(unpack('V*',str_pad($xi,4*ceil(strlen($xi)/4),"\0")));if($Rk)$W[]=strlen($xi);return$W;}function
xxtea_mx($bl,$al,$oj,$Xe){return
int32((($bl>>5&0x7FFFFFF)^$al<<2)+(($al>>3&0x1FFFFFFF)^$bl<<4))^int32(($oj^$al)+($Xe^$bl));}function
encrypt_string($jj,$w){if($jj=="")return"";$w=array_values(unpack("V*",pack("H*",md5($w))));$W=str2long($jj,true);$ig=count($W)-1;$bl=$W[$ig];$al=$W[0];$Vh=floor(6+52/($ig+1));$oj=0;while($Vh-->0){$oj=int32($oj+0x9E3779B9);$xc=$oj>>2&3;for($eh=0;$eh<$ig;$eh++){$al=$W[$eh+1];$hg=xxtea_mx($bl,$al,$oj,$w[$eh&3^$xc]);$bl=int32($W[$eh]+$hg);$W[$eh]=$bl;}$al=$W[0];$hg=xxtea_mx($bl,$al,$oj,$w[$eh&3^$xc]);$bl=int32($W[$ig]+$hg);$W[$ig]=$bl;}return
long2str($W,false);}function
decrypt_string($jj,$w){if($jj=="")return"";if(!$w)return
false;$w=array_values(unpack("V*",pack("H*",md5($w))));$W=str2long($jj,false);$ig=count($W)-1;$bl=$W[$ig];$al=$W[0];$Vh=floor(6+52/($ig+1));$oj=int32($Vh*0x9E3779B9);while($oj){$xc=$oj>>2&3;for($eh=$ig;$eh>0;$eh--){$bl=$W[$eh-1];$hg=xxtea_mx($bl,$al,$oj,$w[$eh&3^$xc]);$al=int32($W[$eh]-$hg);$W[$eh]=$al;}$bl=$W[$ig];$hg=xxtea_mx($bl,$al,$oj,$w[$eh&3^$xc]);$al=int32($W[0]-$hg);$W[0]=$al;$oj=int32($oj-0x9E3779B9);}return
long2str($W,true);}$yh=array();if($_COOKIE["adminer_permanent"]){foreach(explode(" ",$_COOKIE["adminer_permanent"])as$X){list($w)=explode(":",$X);$yh[$w]=$X;}}function
add_invalid_login(){$Ia=get_temp_dir()."/adminer-invalid";foreach(glob("$Ia*")?:array($Ia)as$n){$p=file_open_lock($n);if($p)break;}if(!$p)$p=file_open_lock("$Ia-".rand_string());if(!$p)return;$Le=json_decode(stream_get_contents($p),true);$Oj=time();if($Le){foreach($Le
as$Me=>$X){if($X[0]<$Oj)unset($Le[$Me]);}}$Je=&$Le[adminer()->bruteForceKey()];if(!$Je)$Je=array($Oj+30*60,0);$Je[1]++;file_write_unlock($p,json_encode($Le));}function
check_invalid_login(array&$yh){$Le=array();foreach(glob(get_temp_dir()."/adminer-invalid*")as$n){$p=file_open_lock($n);if($p){$Le=json_decode(stream_get_contents($p),true);file_unlock($p);break;}}$w=adminer()->bruteForceKey();$Je=idx($Le,$w,array());$sg=($Je[1]>29?$Je[0]-time():0);if($sg>0){$k=lang(94,ceil($sg/60));if($_SERVER["HTTP_X_FORWARDED_FOR"]!=""&&$w==$_SERVER["REMOTE_ADDR"])$k
.='<br>'.lang(95,'<b>login-reverse-proxy</b>'," href='https://www.adminer.org/plugins/?version=".VERSION."'".target_blank());auth_error($k,$yh,false);}}function
password_required(){static$K;if($K===null){$K=(bool)get_session("password_required");if(!$K){$Gb=adminer()->credentials();$K=!is_object(Driver::connect($Gb[0],$Gb[1],""));if($K)set_session("password_required",true);}}return$K;}function
require_password_link($G){$eg="<a href='https://www.adminer.org/password/'".target_blank().">".lang(96)."</a>";if(!function_exists('password_hash'))return" $eg";$Ah=($G!==null?$G:base64_encode(substr(pack("H*",rand_string()),0,12)));$Yd=password_hash($Ah,PASSWORD_DEFAULT);$n="adminer-plugins.php";$Uc=file_exists("adminer-plugins.php");if($Uc)$He=($G!==null?lang(97,"<b>$n</b>"):lang(98,"<b>$n</b>","<b>$Ah</b>"));else{$n="<button name='password_less' value='".h($Yd)."' class='link'>$n</button>";$He=($G!==null?lang(99,$n):lang(100,$n,"<b>$Ah</b>"));}$qf="\t<a>new</a> Adminer\\Password(<span class='jush-apo'>'".h($Yd)."'</span>),";$K="<p>$He
<pre><code class='jush'>".($Uc?$qf:"&lt;?php\n<a>return</a> <a>array</a>(\n$qf\n);")."</code></pre>
<p>$eg
";return" <a href='#password-less' class='toggle'>".lang(101)."</a>
<div id='password-less' class='hidden'>".($Uc?$K:"<form action='' method='post'>\n".$K.input_token()."</form>")."</div>";}if(preg_match('~^[-\w$./]+$~',$_POST["password_less"])&&verify_token()){header("Content-Type: application/octet-stream");header("Content-Disposition: attachment; filename=adminer-plugins.php");echo"<?php\nreturn array(\n\tnew Adminer\\Password('$_POST[password_less]'),\n);\n";exit;}$_a=$_POST["auth"];if($_a&&verify_token()){session_regenerate_id();$Mk=$_a["driver"];$O=$_a["server"];$V=$_a["username"];$G=(string)$_a["password"];$i=$_a["db"];set_password($Mk,$O,$V,$G);$_SESSION["db"][$Mk][$O][$V][$i]=true;if($_a["permanent"]){$w=implode("-",array_map('base64_encode',array($Mk,$O,$V,$i)));$Ph=adminer()->permanentLogin(true);$yh[$w]="$w:".base64_encode($Ph?encrypt_string($G,$Ph):"");cookie("adminer_permanent",implode(" ",$yh));}if(!array_diff(array_keys($_POST),array("auth","token"))||$Mk!=DRIVER||$O!=SERVER||$V!==$_GET["username"]||$i!=DB)redirect(auth_url($Mk,$O,$V,$i));}elseif($_POST["logout"]&&(!$_SESSION["token"]||verify_token())){foreach(array("pwds","db","dbs","queries")as$w)set_session($w,null);unset_permanent($yh);redirect(substr(preg_replace('~\b(username|db|ns)=[^&]*&~','',ME),0,-1),lang(102).' '.lang(103));}elseif($yh&&!$_SESSION["pwds"]){session_regenerate_id();$Ph=adminer()->permanentLogin();foreach($yh
as$w=>$X){list(,$fb)=explode(":",$X);list($Mk,$O,$V,$i)=array_map('base64_decode',explode("-",$w));set_password($Mk,$O,$V,decrypt_string(base64_decode($fb),$Ph));$_SESSION["db"][$Mk][$O][$V][$i]=true;}}function
unset_permanent(array&$yh){foreach($yh
as$w=>$X){list($Mk,$O,$V,$i)=array_map('base64_decode',explode("-",$w));if($Mk==DRIVER&&$O==SERVER&&$V==$_GET["username"]&&$i==DB)unset($yh[$w]);}cookie("adminer_permanent",implode(" ",$yh));}function
auth_error($k,array&$yh,$Ke=true){$Mi=session_name();if(isset($_GET["username"])){header("HTTP/1.1 403 Forbidden");if(($_COOKIE[$Mi]||$_GET[$Mi])&&!$_SESSION["token"])$k=lang(104);elseif($Ke&&($G=get_password())!==null){restart_session();add_invalid_login();if($G===false)$k
.=($k?'<br>':'').lang(105,target_blank(),'<code>permanentLogin()</code>');set_password(DRIVER,SERVER,$_GET["username"],null);unset_permanent($yh);}}if(!$_COOKIE[$Mi]&&$_GET[$Mi]&&ini_bool("session.use_only_cookies"))$k=lang(106);$hh=session_get_cookie_params();cookie("adminer_key",($_COOKIE["adminer_key"]?:rand_string()),$hh["lifetime"]);if(!$_SESSION["token"])$_SESSION["token"]=rand(1,1e6);page_header(lang(34),$k,null);echo"<form action='' method='post'>\n","<div>";if(hidden_fields($_POST,array("auth")))echo"<p class='message'>".lang(107)."\n";echo
input_token(),"</div>\n";adminer()->loginForm();echo"</form>\n";page_footer("auth");exit;}if(isset($_GET["username"])&&!class_exists('Adminer\Db')){unset($_SESSION["pwds"][DRIVER]);unset_permanent($yh);page_header(lang(108),lang(109,implode(", ",Driver::$extensions)),false);page_footer("auth");exit;}$e='';if(isset($_GET["username"])&&is_string(get_password())){check_invalid_login($yh);$Gb=adminer()->credentials();$e=Driver::connect($Gb[0],$Gb[1],$Gb[2]);if(is_object($e)){Db::$instance=$e;Driver::$instance=new
Driver($e);if($e->flavor)save_settings(array("vendor-".DRIVER."-".SERVER=>get_driver(DRIVER)));}}$wf=null;if(!is_object($e)||($wf=adminer()->login($_GET["username"],get_password()))!==true){$k=(is_string($e)?nl_br(h($e)):(is_string($wf)?$wf:lang(110))).(preg_match('~^ | $~',get_password())?'<br>'.lang(111):'');auth_error($k,$yh);}if($_POST["logout"]&&$_SESSION["token"]&&!verify_token()){page_header(lang(93),lang(112));page_footer("db");exit;}if(!$_SESSION["token"])$_SESSION["token"]=rand(1,1e6);stop_session(true);if($_a&&$_POST["token"])$_POST["token"]=get_token();$k='';if($_POST){if(!verify_token()){header("HTTP/1.1 403 Forbidden");$k=lang(112).' '.lang(113);}}elseif($_SERVER["REQUEST_METHOD"]=="POST"){header("HTTP/1.1 413 Content Too Large");$k=lang(114,"<b>post_max_size</b>");if(isset($_GET["sql"]))$k
.=' '.lang(115);}function
print_select_result($J,$f=null,array$Ug=array(),&$y=0){$sf=array();$v=array();$d=array();$Oa=array();$mk=array();$K=array();for($r=0;(!$y||$r<$y)&&($L=$J->fetch_row());$r++){if(!$r){echo"<div class='scrollable'>\n","<table class='nowrap odds'>\n","<thead><tr>";for($Ue=0;$Ue<count($L);$Ue++){$l=$J->fetch_field();$C=$l->name;$Tg=(isset($l->orgtable)?$l->orgtable:"");$Sg=(isset($l->orgname)?$l->orgname:$C);if($Ug&&JUSH=="sql")$sf[$Ue]=($C=="table"?"table=":($C=="possible_keys"?"indexes=":null));elseif($Tg!=""){if(isset($l->table))$K[$l->table]=$Tg;if(!isset($v[$Tg])){$v[$Tg]=array();foreach(indexes($Tg,$f)as$u){if($u["type"]=="PRIMARY"){$v[$Tg]=array_flip($u["columns"]);break;}}$d[$Tg]=$v[$Tg];}if(isset($d[$Tg][$Sg])){unset($d[$Tg][$Sg]);$v[$Tg][$Sg]=$Ue;$sf[$Ue]=$Tg;}}if($l->charsetnr==63)$Oa[$Ue]=true;$mk[$Ue]=$l->type;echo"<th title='".h(trim(($Tg!=""?"$Tg.$Sg":($l->name!=$Sg?$Sg:""))." ".driver()->typeName($l)))."'>".h($C).($Ug?doc_link(array('sql'=>"explain-output.html#explain_".strtolower($C),'mariadb'=>"explain/#the-columns-in-explain-select",)):"");}echo"<tbody>\n";}echo"<tr>";foreach($L
as$w=>$X){$z="";if(isset($sf[$w])&&!$d[$sf[$w]]){if($Ug&&JUSH=="sql"){$R=$L[array_search("table=",$sf)];$z=ME.$sf[$w].url_escape($Ug[$R]!=""?$Ug[$R]:$R);}else{$z=ME."edit=".url_escape($sf[$w]);foreach($v[$sf[$w]]as$ib=>$Ue){if($L[$Ue]===null){$z="";break;}$z
.="&where[".url_escape(bracket_escape($ib))."]=".url_escape($L[$Ue]);}}}$l=array('type'=>($Oa[$w]?'blob':($mk[$w]==254?'char':'')),);$X=select_value($X,$z,$l,null);echo"<td".($mk[$w]<=9||$mk[$w]==246?" class='number'":"").">$X";}}$y=$r;echo($r?"</table>\n</div>":"<p class='message'>".lang(15))."\n";return$K;}function
textarea($C,$Y,$M=10,$mb=80){echo"<textarea name='".h($C)."' rows='$M' cols='$mb' class='sqlarea jush-".JUSH."' spellcheck='false' wrap='off'>";if(is_array($Y)){foreach($Y
as$X)echo
h($X[0])."\n\n\n";}else
echo
h($Y);echo"</textarea>";}function
select_input($b,array$Pg,$Y="",$zh=""){if($Pg&&$Y!=""&&!isset($Pg[$Y]))$Pg=array($Y=>$Y)+$Pg;$Ej=($Pg?"select":"input");return"<$Ej$b".($Pg?"><option value=''>$zh".optionlist($Pg,$Y,true)."</select>":" size='10' value='".h($Y)."' placeholder='$zh'>");}function
json_row($w,$X=null,$Mc=true){static$qd=true;if($qd)echo"{";if($w!=""){echo($qd?"":",")."\n\t\"".addcslashes($w,"\r\n\t\"\\/").'": '.($X!==null?($Mc?'"'.addcslashes($X,"\r\n\"\\/").'"':$X):'null');$qd=false;}else{echo"\n}\n";$qd=true;}}function
flat_collations(){$lb=collations();return(is_array(reset($lb))?call_user_func_array('array_merge',array_values($lb)):$lb);}function
edit_type($w,array$l,array$lb,array$xd=array(),array$cd=array()){$U=(string)$l["type"];echo"<td><select name='".h($w)."[type]' class='type' aria-labelledby='label-type'".on_help_value().">";if($U&&!array_key_exists($U,driver()->types())&&!isset($xd[$U])&&!in_array($U,$cd))$cd[]=$U;$kj=driver()->structuredTypes();if($xd)$kj[lang(116)]=$xd;echo
optionlist(array_merge($cd,$kj),$U),"</select><td>","<input name='".h($w)."[length]' value='".h($l["length"])."' size='3'".(!$l["length"]&&preg_match('~var(char|binary)$~',$U)?" class='required'":"")." aria-labelledby='label-length'>","<td class='options'>",($lb?"<input list='collations' name='".h($w)."[collation]'".option_types($U,'('.text_type().')$')." value='".h($l["collation"])."' placeholder='(".lang(117).")'>":''),(driver()->unsigned?"<select name='".h($w)."[unsigned]'".option_types($U,'^$|'.number_type()).'><option>'.optionlist(driver()->unsigned,$l["unsigned"]).'</select>':''),(isset($l['on_update'])?"<select name='".h($w)."[on_update]'".option_types($U,'timestamp|datetime').'>'.optionlist(array(""=>"(".lang(118).")","CURRENT_TIMESTAMP"),(preg_match('~^CURRENT_TIMESTAMP~i',$l["on_update"])?"CURRENT_TIMESTAMP":$l["on_update"])).'</select>':''),($xd?"<select name='".h($w)."[on_delete]'".option_types($U,'`')."><option value=''>(".lang(119).")".optionlist(explode("|",driver()->onActions),$l["on_delete"])."</select> ":" ");}function
option_types($U,$mk){return" data-types='".h($mk)."'".(preg_match("~$mk~",$U)?"":" class='hidden'");}function
process_length($x){$Hc=driver()->enumLength;return(preg_match("~^\\s*\\(?\\s*$Hc(?:\\s*,\\s*$Hc)*+\\s*\\)?\\s*\$~",$x)&&preg_match_all("~$Hc~",$x,$_f)?"(".implode(",",$_f[0]).")":preg_replace('~^[0-9].*~','(\0)',preg_replace('~[^-0-9,+()[\]]~','',$x)));}function
process_in($X){$Hc=driver()->enumLength;if(preg_match("~^\\s*\\(?\\s*$Hc(?:\\s*,\\s*$Hc)*+\\s*\\)?\\s*\$~",$X)&&preg_match_all("~$Hc~",$X,$_f))return"(".implode(", ",$_f[0]).")";$K=array();foreach(explode(",",$X)as$Te)$K[]=q(trim($Te));return"(".implode(", ",$K).")";}function
process_type(array$l,$jb="COLLATE"){return" $l[type]".process_length($l["length"]).(preg_match(number_type(),$l["type"])&&in_array($l["unsigned"],driver()->unsigned)?" $l[unsigned]":"").(preg_match('~'.text_type().'~',$l["type"])&&$l["collation"]?" $jb ".(JUSH=="mssql"?$l["collation"]:q($l["collation"])):"");}function
process_field(array$l,array$kk){if($l["on_update"])$l["on_update"]=str_ireplace("current_timestamp()","CURRENT_TIMESTAMP",$l["on_update"]);return
array(idf_escape(trim($l["field"])),process_type($kk),($l["null"]?" NULL":" NOT NULL"),default_value($l),(preg_match('~timestamp|datetime~',$l["type"])&&$l["on_update"]?" ON UPDATE $l[on_update]":""),(support("comment")&&$l["comment"]!=""?" COMMENT ".q($l["comment"]):""),($l["auto_increment"]?auto_increment():null),);}function
default_value(array$l){if($l["default"]===null)return"";$j=str_replace("\r","",$l["default"]);$Fd=$l["generated"];return(in_array($Fd,driver()->generated)?(JUSH=="mssql"?" AS ($j)".($Fd=="VIRTUAL"?"":" $Fd"):" GENERATED ALWAYS AS ($j) $Fd"):(preg_match('~^GENERATED ~i',$j)?" $j":" DEFAULT ".(preg_match('~char|binary|text|json|enum|set|String~',$l["type"])||preg_match('~^(?![a-z])~i',$j)?(JUSH=="sql"&&preg_match('~text|json~',$l["type"])?"(".q($j).")":q($j)):str_ireplace("current_timestamp()","CURRENT_TIMESTAMP",(JUSH=="sqlite"?"($j)":$j)))));}function
edit_fields(array$m,array$lb,$U="TABLE",array$xd=array()){$m=array_values($m);$Wb=(($_POST?$_POST["defaults"]:get_setting("defaults"))?"":" class='hidden'");$qb=(($_POST?$_POST["comments"]:get_setting("comments"))?"":" class='hidden'");echo"<thead><tr>\n",($U=="PROCEDURE"?"<td>":""),"<th id='label-name'>".($U=="TABLE"?lang(120):lang(121)),"<td id='label-type'>".lang(47)."<textarea id='enum-edit' rows='4' cols='12' wrap='off' hidden></textarea>".script("qs('#enum-edit').onblur = editingLengthBlur;"),"<td id='label-length'>".lang(122),"<td>".lang(123);if($U=="TABLE")echo"<td id='label-null'>NULL\n","<td><input type='radio' name='auto_increment_col' value=''><abbr id='label-ai' title='".lang(49)."'>AI</abbr>",doc_link(array('sql'=>"example-auto-increment.html",'mariadb'=>"auto_increment/",)),"<td id='label-default'$Wb>".lang(50),(support("comment")?"<td id='label-comment'$qb>".lang(48):"");$gf=!support("move_col");echo"<td>".icon("plus","add[".($gf?count($m):0)."]","+",lang(124),($gf?on('click','editingAddLastRow'):"")),"<tbody".on('click','editingClick').on('input','editingInput').on('keydown','editingKeydown').">\n";foreach($m
as$r=>$l){$r++;$Vg=$l[($_POST?"orig":"field")];$ic=(isset($_POST["add"][$r-1])||(isset($l["field"])&&!idx($_POST["drop_col"],$r)))&&(support("drop_col")||$Vg=="");echo"<tr".($ic?"":" hidden").">\n",($U=="PROCEDURE"?"<td>".html_select("fields[$r][inout]",explode("|",driver()->inout),$l["inout"]):"")."<th>",(support("move_col")?icon("move","","↕",lang(125))." ":"");if($ic)echo"<input name='fields[$r][field]' value='".h($l["field"])."' data-maxlength='64' autocapitalize='off' aria-labelledby='label-name'".(isset($_POST["add"][$r-1])?" autofocus":"").">";echo
input_hidden("fields[$r][orig]",$Vg);edit_type("fields[$r]",$l,$lb,$xd);if($U=="TABLE"){echo"<td><label class='block'>".checkbox("fields[$r][null]",1,$l["null"],"","","","label-null")."</label>","<td><label class='block'><input type='radio' name='auto_increment_col' value='$r'".($l["auto_increment"]?" checked":"")." aria-labelledby='label-ai'></label>","<td$Wb>".(driver()->generated?html_select("fields[$r][generated]",array_merge(array("","DEFAULT"),driver()->generated),$l["generated"])." ":checkbox("fields[$r][generated]",1,$l["generated"],"","","","label-default"));$b=" name='fields[$r][default]' aria-labelledby='label-default'";$Y=h($l["default"]);echo(preg_match('~\n~',$l["default"])?"<textarea$b rows='2' cols='30' style='vertical-align: bottom;'>\n$Y</textarea>":"<input$b value='$Y'>");if(support("comment")){$b=" name='fields[$r][comment]' data-maxlength='".(min_version(5.5)?1024:255)."' aria-labelledby='label-comment'";echo"<td$qb>".adminer()->commentInput('COLUMN',$b,$l["comment"]);}}echo"<td>",(support("move_col")?icon("plus","add[$r]","+",lang(124))." ":""),($Vg==""||support("drop_col")?icon("cross","drop_col[$r]","x",lang(126)):"");}}function
process_fields(array&$m){if($_POST["add"]){$m=array_values($m);array_splice($m,key($_POST["add"]),0,array(array()));}return$_POST["add"]||$_POST["drop_col"];}function
drop_create($tc,$g,$uc,$Kj,$vc,$_,$Tf,$Rf,$Sf,$Eg,$pg){if($_POST["drop"])query_redirect($tc,$_,$Tf);elseif($Eg=="")query_redirect($g,$_,$Sf);elseif(support("transaction_ddl")){driver()->begin();queries_redirect($_,$Rf,queries($tc)&&queries($g)&&driver()->commit());driver()->rollback();}elseif($Eg!=$pg){$Fb=queries($g);queries_redirect($_,$Rf,$Fb&&queries($tc));if($Fb)queries($uc);}else
queries_redirect($_,$Rf,queries($Kj)&&queries($vc)&&queries($tc)&&queries($g));}function
create_trigger($Gg,array$L){$Qj=" $L[Timing] $L[Event]".(preg_match('~ OF~',$L["Event"])?" $L[Of]":"");return"CREATE TRIGGER ".idf_escape($L["Trigger"]).(JUSH=="mssql"?$Gg.$Qj:$Qj.$Gg).rtrim(" $L[Type]\n$L[Statement]",";").";";}function
q_dollar($Q){$ac='$$';while(strpos($Q.$ac,$ac)!=strlen($Q))$ac='$_'.substr($ac,1);return$ac.$Q.$ac;}function
routine_collate($kb){static$Ya=array();if($kb&&!$Ya){foreach(collations()as$Xa=>$Jk){foreach((array)$Jk
as$X)$Ya[$X]=$Xa;}}return($Ya[$kb]?"CHARACTER SET ".q($Ya[$kb])." ":"")."COLLATE";}function
create_routine($ti,array$L){$P=array();$m=(array)$L["fields"];ksort($m);foreach($m
as$l){if($l["field"]!="")$P[]=(preg_match("~^(".driver()->inout.")\$~",$l["inout"])?"$l[inout] ":"").idf_escape($l["field"]).process_type($l,routine_collate($l["collation"]));}$Yb=rtrim($L["definition"],";");return"CREATE $ti ".idf_escape(trim($L["name"]))." (".implode(", ",$P).")".($ti=="FUNCTION"?" RETURNS".process_type($L["returns"],routine_collate($L["returns"]["collation"])):"").($L["language"]?" LANGUAGE $L[language]":"").(JUSH=="pgsql"?" AS ".q_dollar("\n".trim($Yb)."\n"):"\n$Yb;");}function
remove_definer($I){return
preg_replace('~^([A-Z =]+) DEFINER=`'.preg_replace('~@(.*)~','`@`(%|\1)',logged_user()).'`~','\1',$I);}function
format_foreign_key(array$o){$i=$o["db"];$ug=$o["ns"];return" FOREIGN KEY (".implode(", ",array_map('Adminer\idf_escape',$o["source"])).") REFERENCES ".($i!=""&&$i!=$_GET["db"]?idf_escape($i).".":"").($ug!=""&&$ug!=$_GET["ns"]?idf_escape($ug).".":"").idf_escape($o["table"])." (".implode(", ",array_map('Adminer\idf_escape',$o["target"])).")".(preg_match("~^(".driver()->onActions.")\$~",$o["on_delete"])?" ON DELETE $o[on_delete]":"").(preg_match("~^(".driver()->onActions.")\$~",$o["on_update"])?" ON UPDATE $o[on_update]":"").($o["deferrable"]?" $o[deferrable]":"");}function
tar_file($n,$Vj){$K=pack("a100a8a8a8a12a12",$n,644,0,0,decoct($Vj->size),decoct(time()));$db=8*32;for($r=0;$r<strlen($K);$r++)$db+=ord($K[$r]);$K
.=sprintf("%06o",$db)."\0 ";echo$K,str_repeat("\0",512-strlen($K));$Vj->send();echo
str_repeat("\0",511-($Vj->size+511)%512);}function
doc_link(array$vh,$Lj="<sup>?</sup>"){$Ki=connection()->server_info;$Nk=preg_replace('~^(\d\.?\d).*~s','\1',$Ki);$_k=array('sql'=>"https://dev.mysql.com/doc/refman/$Nk/en/",'sqlite'=>"https://www.sqlite.org/",'pgsql'=>"https://www.postgresql.org/docs/".(connection()->flavor=='cockroach'?"current":$Nk)."/",'mssql'=>"https://learn.microsoft.com/en-us/sql/",'oracle'=>"https://www.oracle.com/pls/topic/lookup?ctx=db".preg_replace('~^.* (\d+)\.(\d+)\.\d+\.\d+\.\d+.*~s','\1\2',$Ki)."&id=",);if(connection()->flavor=='maria'){$_k['sql']="https://mariadb.com/kb/en/";$vh['sql']=(isset($vh['mariadb'])?$vh['mariadb']:str_replace(".html","/",$vh['sql']));}return($vh[JUSH]?"<a href='".h($_k[JUSH].$vh[JUSH].(JUSH=='mssql'?"?view=sql-server-ver$Nk":""))."'".target_blank().">$Lj</a>":"");}function
db_size($i){if(!connection()->select_db($i))return"?";$K=0;foreach(table_status()as$S)$K+=$S["Data_length"]+$S["Index_length"];return
format_number($K);}function
set_utf8mb4($g){static$P=false;if(!$P&&preg_match('~\butf8mb4~i',$g)){$P=true;echo"SET NAMES ".charset(connection()).";\n\n";}}if(isset($_GET["status"]))$_GET["variables"]=$_GET["status"];if(isset($_GET["import"]))$_GET["sql"]=$_GET["import"];if(DB==""&&isset($_GET["ns"]))redirect(remove_from_uri('ns'));if(!(DB!=""?connection()->select_db(DB):isset($_GET["sql"])||isset($_GET["dump"])||isset($_GET["database"])||isset($_GET["processlist"])||isset($_GET["privileges"])||isset($_GET["user"])||isset($_GET["variables"])||$_GET["script"]=="connect"||$_GET["script"]=="kill")){if(DB!=""||$_GET["refresh"]){restart_session();set_session("dbs",null);}if(DB!=""){header("HTTP/1.1 404 Not Found");page_header(lang(33).": ".h(DB),lang(127),true);}else{if(!isset($_GET["db"])&&support("single_db")){$h=adminer()->databases();if($h)redirect(ME."db=".url_escape($h[0]));}if($_POST["db"]&&!$k)queries_redirect(substr(ME,0,-1),lang(128),drop_databases($_POST["db"]));page_header(lang(129),$k,false);echo"<p class='links'>\n";foreach(array('database'=>lang(130),'privileges'=>lang(69),'processlist'=>lang(131),'variables'=>lang(132),'status'=>lang(133),)as$w=>$X){if(support($w))echo"<a href='".h(ME)."$w='>$X</a>\n";}echo"<p>".lang(134,get_driver(DRIVER),"<b>".h(connection()->server_info)."</b>","<b>".connection()->extension."</b>")."\n","<p>".lang(135,"<b>".h(logged_user())."</b>")."\n";$h=adminer()->databases();if($h){$_i=support("scheme");$lb=collations();echo"<form action='' method='post'>\n","<table class='checkable odds'".on('click','tableClick').on('dblclick','tableClick').">\n","<thead><tr>".(support("database")?"<td class='hover'>":"")."<th".(JUSH!='mssql'?" aria-sort='ascending'":"").">".lang(33).(get_session("dbs")!==null?" - <a href='".h(ME)."refresh=1'>".lang(136)."</a>":"")."<td>".lang(137)."<td>".lang(138)."<td>".lang(139)." - <a href='".h(ME)."dbsize=1'".on('click','ajaxSetHtml',ME."script=connect").">".lang(140)."</a>"."<tbody>\n";$h=($_GET["dbsize"]?count_tables($h):array_flip($h));foreach($h
as$i=>$T){$si=h(preg_replace('~&db=[^&]*~','',ME))."db=".url_escape($i);$s=h("Db-".$i);echo"<tr>".(support("database")?"<td class='hover'>".checkbox("db[]",$i,in_array($i,(array)$_POST["db"]),"","","",$s):""),"<th><a href='$si' id='$s'>".h($i)."</a>";$kb=h(db_collation($i,$lb));echo"<td>".(support("database")?"<a href='$si".($_i?"&amp;ns=":"")."&amp;database=' title='".lang(65)."'>$kb</a>":$kb),"<td align='right'><a href='$si&amp;schema=' id='tables-".h($i)."' title='".lang(68)."'>".($_GET["dbsize"]?$T:"?")."</a>","<td align='right' id='size-".h($i)."'>".($_GET["dbsize"]?db_size($i):"?"),"\n";}echo"</table>\n",(support("database")?"<div class='footer'><div>\n"."<fieldset><legend>".lang(141)." <span id='selected'></span></legend><div>\n"."<input type='hidden' name='all' value=''".on('click','countDbs').">\n"."<input type='submit' name='drop' value='".lang(142)."'".confirm().">\n"."</div></fieldset>\n"."</div></div>\n":""),input_token(),"</form>\n",script("tableCheck();");}$ja=adminer();$Ch=($ja
instanceof
Plugins?$ja->plugins:array());$sc=($ja
instanceof
Plugins?$ja->drivers:array());$fc=design_checksums();if($Ch||$sc||$fc){$eb=($ja
instanceof
Plugins?$ja->checksums():array());$yg=Plugins::officialChecksums();$wk=function($zk){return" (<a href='$zk'".target_blank()." class='update'>".VERSION."</a>)";};$Bh=function($kd)use($eb,$yg,$wk){return($eb[$kd]&&$yg[$kd]&&$eb[$kd]!==$yg[$kd]?$wk("https://www.adminer.org/plugins/?version=".VERSION):"");};echo"<div class='plugins'>\n","<h3>".lang(143)."</h3>\n<ul>\n";foreach($Ch
as$_h){$gi=new
\ReflectionObject($_h);$cc=(method_exists($_h,'description')?$_h->description():"");if(!$cc){if(preg_match('~^/[\s*]+(.+)~',$gi->getDocComment(),$A))$cc=$A[1];}$Ai=(method_exists($_h,'screenshot')?$_h->screenshot():"");echo"<li><b>".get_class($_h)."</b>".h($cc?": $cc":"").($Ai?" (<a href='".h($Ai)."'".target_blank().">".lang(144)."</a>)":"").$Bh(basename((string)$gi->getFileName(),'.php'))."\n";}foreach($sc
as$s=>$C)echo"<li><b>".h($s)."</b>: ".h($C).$Bh(basename((string)$ja->driverFiles[$s],'.php'))."\n";if($fc){$_g=official_design_checksums();foreach($fc
as$n=>$ec){list($C,$db)=$ec;$zg=$_g["$C/$n"];echo"<li><b>".h($n)."</b>".h($C?": $C":"").($zg&&$zg!==$db?$wk("https://www.adminer.org/?version=".VERSION."#extras"):"")."\n";}}echo"</ul>\n";adminer()->pluginsLinks();echo"</div>\n";}}page_footer("db");exit;}adminer()->afterConnect();class
TmpFile{private$handler;var$size=0;function
__construct(){$this->handler=tmpfile();}function
write($yb){$this->size+=strlen($yb);fwrite($this->handler,$yb);}function
send(){fseek($this->handler,0);fpassthru($this->handler);fclose($this->handler);}}if($_GET["select"]!=""&&($_POST["edit"]||$_POST["clone"])&&!$_POST["save"])$_GET["edit"]=$_GET["select"];if(isset($_GET["callf"]))$_GET["call"]=$_GET["callf"];if(isset($_GET["function"]))$_GET["procedure"]=$_GET["function"];if(isset($_GET["download"])){$a=$_GET["download"];$m=fields($a);header("Content-Type: application/octet-stream");header("Content-Disposition: attachment; filename=".friendly_url("$a-".implode("_",$_GET["where"])).".".friendly_url($_GET["field"]));$N=array(idf_escape($_GET["field"]));$J=driver()->select($a,$N,array(where($_GET,$m)),$N);$L=($J?$J->fetch_row():array());echo
driver()->value($L[0],$m[$_GET["field"]]);exit;}elseif(isset($_GET["table"])){$a=$_GET["table"];$m=fields($a);if(!$m)$k=adminer()->error()?:lang(12);$S=table_status1($a);$C=adminer()->tableName($S);page_header(($m&&is_view($S)?$S['Engine']=='materialized view'?lang(145):lang(146):lang(147)).": ".($C!=""?$C:h($a)),$k);$ri=array();foreach($m
as$w=>$l)$ri+=$l["privileges"];adminer()->selectLinks($S,(isset($ri["insert"])||!support("table")?"":null));$pb=$S["Comment"];if($pb!="")echo"<p class='nowrap'>".lang(48).": ".adminer()->commentValue('TABLE',$pb)."\n";if($m)adminer()->tableStructurePrint($m,$S);function
tables_links(array$T){echo"<ul>\n";foreach($T
as$L){$z=preg_replace('~ns=[^&]*~',"ns=".url_escape($L["ns"]),ME);echo"<li><a href='".h($z."table=".url_escape($L["table"]))."'>".($L["ns"]!=$_GET["ns"]?"<b>".h($L["ns"])."</b>.":"").h($L["table"])."</a>";}echo"</ul>\n";}$Ae=driver()->inheritsFrom($a);if($Ae){echo"<h3>".lang(148)."</h3>\n";tables_links($Ae);}if(support("indexes")&&driver()->supportsIndex($S)){echo"<div>\n","<h3 id='indexes'>".lang(149)."</h3>\n";$v=indexes($a);if($v)adminer()->tableIndexesPrint($v,$S);if(driver()->supportsAlterIndex($S))echo'<p class="links hover"><a href="'.h(ME).'indexes='.url_escape($a).'">'.lang(150)."</a>\n";echo"</div>\n";}if(!is_view($S)){if(fk_support($S)){echo"<div>\n","<h3 id='foreign-keys'>".lang(116)."</h3>\n";$xd=foreign_keys($a);if($xd){echo"<table>\n","<thead><tr><th>".lang(151)."<td>".lang(152)."<td>".lang(119)."<td>".lang(118)."<td class='hover'><tbody>\n";foreach($xd
as$C=>$o){echo"<tr title='".h($C)."'>","<th><i>".implode("</i>, <i>",array_map('Adminer\h',$o["source"]))."</i>";$z=($o["db"]!=""?preg_replace('~db=[^&]*~',"db=".url_escape($o["db"]),ME):($o["ns"]!=""?preg_replace('~ns=[^&]*~',"ns=".url_escape($o["ns"]),ME):ME));echo"<td><a href='".h($z."table=".url_escape($o["table"]))."'>".($o["db"]!=""&&$o["db"]!=DB?"<b>".h($o["db"])."</b>.":"").($o["ns"]!=""&&$o["ns"]!=$_GET["ns"]?"<b>".h($o["ns"])."</b>.":"").h($o["table"])."</a>","(<i>".implode("</i>, <i>",array_map('Adminer\h',$o["target"]))."</i>)","<td>".h($o["on_delete"]),"<td>".h($o["on_update"]),'<td class="hover"><a href="'.h(ME.'foreign='.url_escape($a).'&name='.url_escape($C)).'">'.lang(153).'</a>',"\n";}echo"</table>\n";}echo'<p class="links hover"><a href="'.h(ME).'foreign='.url_escape($a).'">'.lang(154)."</a>\n","</div>\n";}if(support("check")){echo"<div>\n","<h3 id='checks'>".lang(155)."</h3>\n";$ab=driver()->checkConstraints($a);if($ab){echo"<table>\n";foreach($ab
as$w=>$X)echo"<tr title='".h($w)."'>","<td><code class='jush-".JUSH."'>".shorten_utf8(preg_replace('~\s+~',' ',ltrim($X)),80,"</code>"),"<td class='hover'><a href='".h(ME.'check='.url_escape($a).'&name='.url_escape($w))."'>".lang(153)."</a>","\n";echo"</table>\n";}echo'<p class="links hover"><a href="'.h(ME).'check='.url_escape($a).'">'.lang(156)."</a>\n","</div>\n";}}if(support(is_view($S)?"view_trigger":"trigger")){echo"<div>\n","<h3 id='triggers'>".lang(157)."</h3>\n";$hk=triggers($a);if($hk){echo"<table>\n";foreach($hk
as$w=>$X)echo"<tr valign='top'><td>".h($X[0])."<td>".h($X[1])."<th>".h($w)."<td class='hover'><a href='".h(ME.'trigger='.url_escape($a).'&name='.url_escape($w))."'>".lang(153)."</a>\n";echo"</table>\n";}echo'<p class="links hover"><a href="'.h(ME).'trigger='.url_escape($a).'">'.lang(158)."</a>\n","</div>\n";}$_e=driver()->inheritedTables($a);if($_e){echo"<h3 id='partitions'>".lang(159)."</h3>\n";$kh=driver()->partitionsInfo($a);if($kh)echo"<p><code class='jush-".JUSH."'>BY ".h("$kh[partition_by]($kh[partition])")."</code>\n";tables_links($_e);}}elseif(isset($_GET["schema"])){page_header(lang(68),"",array(),h(DB.($_GET["ns"]?".$_GET[ns]":"")));function
schema_column($R,array$fi,array&$d){if(!isset($d[$R])){$d[$R]=0;foreach((array)idx($fi,$R)as$C=>$hi){if($C!=$R)$d[$R]=max($d[$R],schema_column($C,$fi,$d)+1);}}return$d[$R];}function
type_class($U){foreach(array('char'=>'text','date'=>'time|year','binary'=>'blob','enum'=>'set',)as$w=>$X){if(preg_match("~$w|$X~",$U))return" class='$w'";}}$wj=array();$yj=array();$xj=array();$hd=array();$da=($_GET["schema"]?:$_COOKIE["adminer_schema-".str_replace(".","_",DB)]);preg_match_all('~([^:]+):([-0-9.]+)x([-0-9.]+)(_|$)~',$da,$_f,PREG_SET_ORDER);foreach($_f
as$r=>$A){$wj[$A[1]]=array((float)$A[2],(float)$A[3]);$yj[]="\n\t'".js_escape($A[1])."': [ $A[2], $A[3] ]";}$zi=array();$fi=array();$xd=array();$pa=driver()->allFields();$ee=array();$zj=array();foreach(table_status('',true)as$R=>$S){if(!is_view($S)){if(adminer()->tableName($S)!="")$zj[$R]=$S;else$ee[$R]=true;}}foreach($zj
as$R=>$S){$H=0;$zi[$R]["fields"]=array();foreach($pa[$R]as$l){$H+=1.25;$hd[$R][$l["field"]]=$H;$zi[$R]["fields"][$l["field"]]=$l;}foreach(adminer()->foreignKeys($R)as$X){if($X["db"]==""&&$X["ns"]==""&&!$ee[$X["table"]]){$xd[$R][]=$X;$fi[$X["table"]][$R]=array();}}}$d=array();$Jd=array();$Zk=array();$Pd=array();foreach(array_keys($zi)as$C)schema_column($C,$fi,$d);arsort($d);foreach($d
as$C=>$c){$Zf=null;foreach((array)idx($xd,$C)as$X){if($X["table"]!=$C&&$zi[$X["table"]])$Zf=($Zf===null?$d[$X["table"]]:min($Zf,$d[$X["table"]]));}$d[$C]=max($c,(int)$Zf-1);}foreach($zi
as$C=>$R){$c=$d[$C];$Jd[$c][]=$C;$Nj=.75*strlen($C);foreach($R["fields"]as$l)$Nj=max($Nj,.65*strlen($l["field"]));$Zk[$c]=max(idx($Zk,$c,0),ceil($Nj)+1);}foreach($xd
as$C=>$Jk){foreach($Jk
as$X){$Od=$d[$C]+(idx($d,$X["table"],$d[$C])>$d[$C]?1:0);$Pd[$Od]=idx($Pd,$Od,0)+1;}}ksort($Jd);$ce=0;$Yk=0;$nb=0;$Mh=null;$uj=array();$Aj=array();foreach($Jd
as$c=>$T){if($Mh!==null){$nb=round($nb+$Zk[$Mh]+1.7+idx($Pd,$c,0)*.1,1);$E=array();foreach($T
as$C){$oj=0;$Db=0;$mg=array_keys((array)idx($fi,$C));foreach((array)idx($xd,$C)as$X)$mg[]=$X["table"];foreach($mg
as$jg){if($zi[$jg]&&$d[$jg]<$c){$oj+=$zi[$jg]["pos"][0];$Db++;}}$E[$C]=($Db?$oj/$Db:$ce);}asort($E);$T=array_keys($E);}$Yj=0;foreach($T
as$C){$H=1.25*count($zi[$C]["fields"]);$zi[$C]["pos"]=($wj[$C]?:array($Yj,$nb));$uj[$C]=$zi[$C]["pos"][1];$Aj[$C]=$Zk[$c];$Yj+=2.5+$H;$ce=max($ce,$zi[$C]["pos"][0]+2.5+$H);$Yk=max($Yk,round($zi[$C]["pos"][1]+$Zk[$c],1));if(!$wj[$C])$xj[]="\n\t'".js_escape($C)."': [ ".$zi[$C]["pos"][0].", ".$zi[$C]["pos"][1]." ]";}$Mh=$c;}$kf=array();$Ja=array();foreach($xd
as$C=>$Jk){foreach($Jk
as$X){$Gj=idx($uj,$X["table"],$uj[$C]);$Xi=$uj[$C]+$Aj[$C];$qi=($Gj-1>$Xi);$if=($qi?$Xi+1:min($uj[$C],$Gj)-1);$Ia=idx($Ja,(string)$if,0);$Ja[(string)$if]=$Ia+1;$if=round($qi?min($if+$Ia*.1,$Gj-1):$if-$Ia*.1,1);while($kf[(string)$if])$if-=.0001;$zi[$C]["references"][$X["table"]][(string)$if]=array($X["source"],$X["target"]);$fi[$X["table"]][$C][(string)$if]=$X["target"];$kf[(string)$if]=true;}}echo'<div id="schema" style="height: ',$ce,'em; width: ',$Yk,'em;">
<script',nonce(),'>
const tablePos = {',implode(",",$yj)."\n",'};
const tablePosDefault = {',implode(",",$xj)."\n",'};
const em = qs(\'#schema\').offsetHeight / ',$ce,';
document.onmousemove = schemaMousemove;
document.onmouseup = event => schemaMouseup(event, \'',js_escape(DB),'\');
</script>
';foreach($zi
as$C=>$R){echo"<div class='table'".on('mousedown','schemaMousedown')." style='top: ".$R["pos"][0]."em; left: ".$R["pos"][1]."em; width: ".$Aj[$C]."em;'>",'<a href="'.h(ME).'table='.url_escape($C).'"><b>'.h($C)."</b></a>";foreach($R["fields"]as$l){$X='<span'.type_class($l["type"]).' title="'.h($l["type"].($l["length"]?"($l[length])":"").($l["null"]?" NULL":'')).'">'.h($l["field"]).'</span>';echo"<br>".($l["primary"]?"<i>$X</i>":$X);}foreach((array)$R["references"]as$Hj=>$hi){foreach($hi
as$if=>$ci){$jf=$if-$R["pos"][1];$lj=($jf>0?"left: 100%; width: calc($jf"."em - 100%)":"left: $jf"."em");$Yk=($jf>0?"100%":(-$jf)."em");$r=0;foreach($ci[0]as$Wi)echo"\n<div class='references' title='".h($Hj)."' id='refs$if-".($r++)."' style='$lj"."; top: ".$hd[$C][$Wi]."em; padding-top: .5em;'>"."<div style='border-top: 1px solid gray; width: $Yk;'></div></div>";}}foreach((array)$fi[$C]as$Hj=>$hi){foreach($hi
as$if=>$Ij){$jf=$if-$R["pos"][1];$r=0;foreach($Ij
as$Fj)echo"\n<div class='references arrow' title='".h($Hj)."' id='refd$if-".($r++)."' style='left: $jf"."em; top: ".$hd[$C][$Fj]."em;'>"."<div style='height: .5em; border-bottom: 1px solid gray; width: ".(-$jf)."em;'></div>"."</div>";}}echo"\n</div>\n";}foreach($zi
as$C=>$R){foreach((array)$R["references"]as$Hj=>$hi){if($zi[$Hj]){foreach($hi
as$if=>$ci){$ag=$ce;$Hf=-10;foreach($ci[0]as$w=>$Wi){$Eh=$R["pos"][0]+$hd[$C][$Wi];$Fh=$zi[$Hj]["pos"][0]+$hd[$Hj][$ci[1][$w]];$ag=min($ag,$Eh,$Fh);$Hf=max($Hf,$Eh,$Fh);}echo"<div class='references' id='refl$if' style='left: $if"."em; top: $ag"."em; padding: .5em 0;'><div style='border-right: 1px solid gray; margin-top: 1px; height: ".($Hf-$ag)."em;'></div></div>\n";}}}}echo'</div>
<p class="links"><a href="',h(ME."schema=".url_escape($da)),'" id="schema-link">',lang(160),'</a>
';}elseif(isset($_GET["dump"])){$a=$_GET["dump"];if($_POST&&!$k){$j=array("auto_increment"=>'');foreach(array("type","routine","event","trigger")as$qj){if(support($qj))$j[$qj."s"]='';}save_settings(array_intersect_key($_POST+$j,array_flip(array("output","format","db_style","table_style","data_style"))+$j),"adminer_export");$T=array_flip((array)$_POST["tables"])+array_flip((array)$_POST["data"]);$Yc=dump_headers((count($T)==1?key($T):DB),(DB==""||$_GET["ns"]===""||count($T)>1));$Qe=preg_match('~sql~',$_POST["format"]);if($Qe){echo"-- Adminer ".VERSION." ".get_driver(DRIVER)." ".str_replace("\n"," ",connection()->server_info)." dump\n\n";if(JUSH=="sql"){echo"SET NAMES utf8;
SET time_zone = '+00:00';
SET foreign_key_checks = 0;
".($_POST["data_style"]?"SET sql_mode = 'NO_AUTO_VALUE_ON_ZERO';
":"")."
";connection()->query("SET time_zone = '+00:00'");connection()->query("SET sql_mode = ''");}}$lj=$_POST["db_style"];$h=array(DB);if(DB==""){$h=$_POST["databases"];if(is_string($h))$h=explode("\n",rtrim(str_replace("\r","",$h),"\n"));}foreach((array)$h
as$i){adminer()->dumpDatabase($i);if(connection()->select_db($i)){if($Qe&&$lj)echo
use_sql($i,$lj).";\n\n";foreach(($_GET["ns"]===""?(array)$_POST["schemas"]:(DB!=""||!support("scheme")?array(""):adminer()->schemas()))as$zi){if($zi!=""){if(DB==""&&information_schema(DB,$zi))continue;set_schema($zi);}$ij=($_POST["table_style"]||$_POST["data_style"]?table_status('',true):array());$Xc=array();$Pb=array();foreach($ij
as$C=>$S){if(DB==""||$_GET["ns"]===""||in_array($C,(array)$_POST["tables"]))$Xc[$C]=$S;if(DB==""||$_GET["ns"]===""||in_array($C,(array)$_POST["data"]))$Pb[$C]=$S;}if($Qe){if($_POST["table_style"]=="DROP+CREATE"&&function_exists('Adminer\drop_sql'))echo
drop_sql($Xc);if($_POST["data_style"]=="TRUNCATE+INSERT"&&function_exists('Adminer\truncate_all_sql')){$ik=array();foreach($Pb
as$C=>$S){if(!is_view($S)&&!($_POST["table_style"]=="DROP+CREATE"&&isset($Xc[$C])))$ik[]=$C;}echo
truncate_all_sql($ik);}$ch="";if($_POST["types"]){foreach(types()as$s=>$U){$Yb=type_definition($s);$wg=($Yb["kind"]=='d'?"DOMAIN":"TYPE");if($Yb["definition"])$ch
.=($lj!='DROP+CREATE'?"DROP $wg IF EXISTS ".idf_escape($U).";;\n":"")."CREATE $wg ".idf_escape($U)." $Yb[definition];\n\n";else$ch
.="-- Could not export type $U\n\n";}}if($_POST["routines"]){foreach(routines()as$L){$C=$L["ROUTINE_NAME"];$ti=$L["ROUTINE_TYPE"];$g=create_routine($ti,array("name"=>$C)+routine($L["SPECIFIC_NAME"],$ti));set_utf8mb4($g);$ch
.=($lj!='DROP+CREATE'?"DROP $ti IF EXISTS ".idf_escape($C).";;\n":"")."$g;\n\n";}}if($_POST["events"]){foreach(get_rows("SHOW EVENTS",null,"-- ")as$L){$g=remove_definer(get_val("SHOW CREATE EVENT ".idf_escape($L["Name"]),3));set_utf8mb4($g);$ch
.=($lj!='DROP+CREATE'?"DROP EVENT IF EXISTS ".idf_escape($L["Name"]).";;\n":"")."$g;;\n\n";}}echo($ch&&JUSH=='sql'?"DELIMITER ;;\n\n$ch"."DELIMITER ;\n\n":$ch);}if($_POST["table_style"]||$_POST["data_style"]){$Pk=array();foreach($ij
as$C=>$S){$R=array_key_exists($C,$Xc);$Nb=array_key_exists($C,$Pb);if($R||$Nb){$Vj=null;if($Yc=="tar"){$Vj=new
TmpFile;ob_start(array($Vj,'write'),1e5);}adminer()->dumpTable($C,($R?$_POST["table_style"]:""),(is_view($S)?2:0));if(is_view($S))$Pk[]=$C;elseif($Nb){$m=fields($C);$N=array("*");$Ab=convert_fields($m,$m);if($Ab)$N[]=substr($Ab,2);adminer()->dumpData($C,$_POST["data_style"],"",$N);}if($Qe&&$_POST["triggers"]&&$R&&($hk=trigger_sql($C)))echo"\nDELIMITER ;;\n$hk\nDELIMITER ;\n";if($Yc=="tar"){ob_end_flush();tar_file((DB!=""?"":"$i/")."$C.csv",$Vj);}elseif($Qe)echo"\n";}}if($Qe&&$_POST["table_style"]&&function_exists('Adminer\foreign_keys_sql')){foreach($Xc
as$C=>$S){if(!is_view($S))echo
foreign_keys_sql($C);}}if($Qe){foreach($Pk
as$Ok)adminer()->dumpTable($Ok,$_POST["table_style"],1);}if($Yc=="tar")echo
pack("x1024");}}}}adminer()->dumpFooter();exit;}page_header(lang(74),$k,($_GET["export"]!=""?array("table"=>$_GET["export"]):array()),h(DB));echo'
<form action="" method="post">
<table class="layout">
';$Sb=array('','USE','DROP+CREATE','CREATE');$_j=array('','DROP+CREATE','CREATE');$Ob=array('','TRUNCATE+INSERT','INSERT');if(JUSH=="sql")$Ob[]='INSERT+UPDATE';$L=get_settings("adminer_export");if(!$L)$L=array("output"=>"text","format"=>"sql","db_style"=>(DB!=""?"":"CREATE"),"table_style"=>"DROP+CREATE","data_style"=>"INSERT");echo"<tr><th>".lang(161)."<td>".html_radios("output",adminer()->dumpOutput(),$L["output"])."\n","<tr><th>".lang(162)."<td>".html_radios("format",adminer()->dumpFormat(),$L["format"])."\n",(JUSH=="sqlite"?"":"<tr><th>".lang(33)."<td>".html_select('db_style',$Sb,$L["db_style"]).(support("type")?checkbox("types",1,$L["types"],lang(7)):"").(support("routine")?checkbox("routines",1,$L["routines"],lang(70)):"").(support("event")?checkbox("events",1,$L["events"],lang(72)):"")),"<tr><th>".lang(138)."<td>".html_select('table_style',$_j,$L["table_style"]).checkbox("auto_increment",1,$L["auto_increment"],lang(49)).(support("trigger")?checkbox("triggers",1,$L["triggers"],lang(157)):""),"<tr><th>".lang(163)."<td>".html_select('data_style',$Ob,$L["data_style"]),'</table>
';adminer()->dumpPrint();echo'<p><input type=\'submit\' value=\'',lang(74),'\'>
',input_token(),'
<table',on('click','dumpClick'),'>
';$Lh=array();if($_GET["ns"]===""){echo"<thead><tr><th style='text-align: left;'>","<label class='block'><input type='checkbox' id='check-schemas' checked class='jsonly' title='".lang(164)."'".on('click','formCheck','^schemas\[').">".lang(165)."</label>","<tbody>\n";foreach(adminer()->schemas()as$zi){if(!information_schema(DB,$zi))echo"<tr><td>".checkbox("schemas[]",$zi,true,$zi,"","block")."\n";}}elseif(DB!=""){$bb=($a!=""?"":" checked");echo"<thead><tr>","<th style='text-align: left;'><label class='block'><input type='checkbox' id='check-tables'$bb class='jsonly' title='".lang(164)."'".on('click','formCheck','^tables\[').">".lang(147)."</label>","<th style='text-align: right;'><label class='block'>".lang(163)."<input type='checkbox' id='check-data'$bb class='jsonly' title='".lang(164)."'".on('click','formCheck','^data\[')."></label>","<tbody>\n";$Pk="";$Cj=tables_list();foreach($Cj
as$C=>$U){$Kh=preg_replace('~_.*~','',$C);$bb=($a==""||$a==(substr($a,-1)=="%"?"$Kh%":$C));$Oh="<tr><td>".checkbox("tables[]",$C,$bb,$C,"","block");if($U!==null&&!preg_match('~table~i',$U))$Pk
.="$Oh\n";else
echo"$Oh<td align='right'><label class='block'><span id='Rows-".h($C)."'></span>".checkbox("data[]",$C,$bb)."</label>\n";$Lh[$Kh]++;}echo$Pk;if($Cj)echo
script("ajaxSetHtml('".js_escape(ME)."script=db');");}else{$h=adminer()->databases();echo"<thead><tr><th style='text-align: left;'>","<label class='block'>".($h?"<input type='checkbox' id='check-databases'".($a==""?" checked":"")." class='jsonly' title='".lang(164)."'".on('click','formCheck','^databases\[').">":"").lang(33)."</label>","<tbody>\n";if($h){foreach($h
as$i){if(!information_schema($i)){$Kh=preg_replace('~_.*~','',$i);echo"<tr><td>".checkbox("databases[]",$i,$a==""||$a=="$Kh%",$i,"","block")."\n";$Lh[$Kh]++;}}}else
echo"<tr><td><textarea name='databases' rows='10' cols='20'></textarea>";}echo'</table>
</form>
';$qd=true;foreach($Lh
as$w=>$X){if($w!=""&&$X>1){echo($qd?"<p>":" ")."<a href='".h(ME)."dump=".url_escape("$w%")."'>".h($w)."</a>";$qd=false;}}}elseif(isset($_GET["privileges"])){page_header(lang(69));echo'<p class="links"><a href="'.h(ME).'user=">'.lang(166)."</a>";$J=connection()->query("SELECT User, Host FROM mysql.".(DB==""?"user":"db WHERE ".q(DB)." LIKE Db")." ORDER BY Host, User");$Hd=$J;if(!$J)$J=connection()->query("SELECT SUBSTRING_INDEX(CURRENT_USER, '@', 1) AS User, SUBSTRING_INDEX(CURRENT_USER, '@', -1) AS Host");echo"<form action=''><p>\n";hidden_fields_get();echo
input_hidden("db",DB),($Hd?"":input_hidden("grant")),"<table class='odds'>\n","<thead><tr><th>".lang(31)."<th>".lang(29)."<td class='hover'><tbody>\n";while($L=$J->fetch_assoc())echo'<tr><td>'.h($L["User"]),"<td>".h($L["Host"]),'<td class="hover"><a href="'.h(ME.'user='.url_escape($L["User"]).'&host='.url_escape($L["Host"])).'">'.lang(13)."</a>\n";if(!$Hd||DB!="")echo"<tr><td><input name='user' autocapitalize='off'>","<td><input name='host' value='localhost' autocapitalize='off'>","<td class='hover'><input type='submit' value='".lang(13)."'>\n";echo"</table>\n","</form>\n";}elseif(isset($_GET["sql"])){if(!$k&&$_POST["export"]){save_settings(array("output"=>$_POST["output"],"format"=>$_POST["format"]),"adminer_import");dump_headers("sql");if($_POST["format"]=="sql")echo"$_POST[query]\n";else{adminer()->dumpTable("","");adminer()->dumpData("","table",$_POST["query"]);adminer()->dumpFooter();}exit;}restart_session();$ge=&get_session("queries");$fe=&$ge[DB];if(!$k&&$_POST["clear"]){$fe=array();redirect(remove_from_uri("history"));}stop_session();$ka=get_settings("adminer_import");if($_POST&&$ka)save_settings($ka,"adminer_import");page_header((isset($_GET["import"])?lang(73):lang(62)),$k);$rf=driver()->lineComment();if(!$k&&$_POST&&!(isset($_GET["import"])&&adminer()->importProcess())){$ac=driver()->delimiter;$p=false;if(!isset($_GET["import"]))$I=$_POST["query"];elseif($_POST["webfile"]){$aj=adminer()->importServerPath();$p=@fopen((file_exists($aj)?$aj:"compress.zlib://$aj.gz"),"rb");$I=($p?fread($p,1e6):false);}else$I=get_file("sql_file",true,$ac);if(is_string($I)){if(($Pf=ini_bytes("memory_limit"))!="-1")ini_set("memory_limit",max($Pf,strval(2*strlen($I)+memory_get_usage()+8e6)));if($I!=""&&strlen($I)<1e6){$Vh=$I.(preg_match("~$ac\\s*\$~",$I)?"":$ac);if(!$fe||first(end($fe))!=$Vh){restart_session();$fe[]=array($Vh,time());set_session("queries",$ge);stop_session();}}$Yi="(?:\\s|/\\*[\s\S]*?\\*/|(?:$rf)[^\n]*\n?|--\r?\n)";$Ag=0;$Dc=true;$Cb=false;$f=connect();if($f&&DB!=""){$f->select_db(DB);if($_GET["ns"]!="")set_schema($_GET["ns"],$f);}$ob=0;$Kc=array();$ih='[\'"'.(JUSH=="sql"?'`':(JUSH=="sqlite"?'`[':(JUSH=="mssql"?'[':''))).']|/\*|'.$rf.'|$'.(JUSH=="pgsql"?'|\$([a-zA-Z]\w*)?\$':'');$Zj=microtime(true);while($I!=""){if(!$Ag&&preg_match("~^$Yi*+DELIMITER\\s+(\\S+)~i",$I,$A)){$ac=preg_quote($A[1]);$I=substr($I,strlen($A[0]));}elseif(!$Ag&&JUSH=='pgsql'&&preg_match("~^($Yi*+COPY\\s+)[^;]+\\s+FROM\\s+stdin;~i",$I,$A)){$ac="\n\\\\\\.\r?\n";$Cb=true;$Ag=strlen($A[0]);}else{preg_match("($ac\\s*|$ih)",$I,$A,PREG_OFFSET_CAPTURE,$Ag);list($zd,$H)=$A[0];if(!$zd&&$p&&!feof($p))$I
.=fread($p,1e5);else{if(!$zd&&rtrim($I)=="")break;$Ag=$H+strlen($zd);if($zd&&!preg_match("(^$ac)",$zd)){$Ta=driver()->hasCStyleEscapes()||(JUSH=="pgsql"&&($H>0&&strtolower($I[$H-1])=="e"));$wh=($zd=='/*'?'\*/':($zd=='['?']':(preg_match("~^(?:$rf)~",$zd)?"\n":preg_quote($zd).($Ta?'|\\\\.':''))));while(preg_match("($wh|\$)s",$I,$A,PREG_OFFSET_CAPTURE,$Ag)){$xi=$A[0][0];if(!$xi&&$p&&!feof($p))$I
.=fread($p,1e5);else{$Ag=$A[0][1]+strlen($xi);if(!$xi||$xi[0]!="\\")break;}}}else{$Dc=false;$Vh=substr($I,0,$H+($Cb?3:0));$ob++;$Oh="<pre id='sql-$ob'><code class='jush-".JUSH."'>".adminer()->sqlCommandQuery($Vh)."</code></pre>\n";if(JUSH=="sqlite"&&preg_match("~^$Yi*+(ATTACH|VACUUM\\b.*\\bINTO)\\b~is",$Vh,$A)!==0){echo$Oh,"<p class='error'>".lang(167,preg_match('~ATTACH~i',$A[1])?'ATTACH':'VACUUM INTO')."\n";$Kc[]=" <a href='#sql-$ob'>$ob</a>";if($_POST["error_stops"])break;}else{if(!$_POST["only_errors"]){echo$Oh;ob_flush();flush();}$fj=microtime(true);if(connection()->multi_query($Vh)&&$f&&preg_match("~^$Yi*+USE\\b~i",$Vh))$f->query($Vh);do{$J=connection()->store_result();if(connection()->error){echo($_POST["only_errors"]?$Oh:""),"<p class='error'>".lang(168).(connection()->errno?" (".connection()->errno.")":"").": ".adminer()->error()."\n";$Kc[]=" <a href='#sql-$ob'>$ob</a>";if($_POST["error_stops"])break
2;}else{$z=ME."sql=".url_escape(trim($Vh));$Oj=" <span class='time'>(".format_time($fj).")</span>".(strlen($z)<1900?" <a href='".h($z)."'>".lang(13)."</a>":"");$ma=connection()->affected_rows;$Sk=($_POST["only_errors"]?"":driver()->warnings());$Tk="warnings-$ob";if($Sk)$Oj
.=", <a href='#$Tk' class='toggle'>".lang(44)."</a>";$Vc=null;$Ug=null;$Wc="explain-$ob";if(is_object($J)){$y=$_POST["limit"];$vg=$y;$Ug=print_select_result($J,$f,array(),$vg);if(!$_POST["only_errors"]){echo"<form action='' method='post'>\n";$vg=max($J->num_rows,$vg);echo"<p class='sql-footer'>".($vg?($y&&$vg>$y?lang(169,$y):"").lang(170,$vg):""),$Oj;if($f&&preg_match("~^($Yi|\\()*+SELECT\\b~i",$Vh)&&($Vc=explain($f,$Vh)))echo", <a href='#$Wc' class='toggle'>Explain</a>";$s="export-$ob";echo", <a href='#$s' class='toggle'>".lang(74)."</a><span id='$s' class='hidden'>: ".html_select("output",adminer()->dumpOutput(),$ka["output"])." ".html_select("format",adminer()->dumpFormat(),$ka["format"]).input_hidden("query",$Vh)."<input type='submit' name='export' value='".lang(74)."'".($y?"":on('click','sqlExport')).">".input_token()."</span>\n"."</form>\n";}}else{if(preg_match("~^$Yi*+(CREATE|DROP|ALTER)$Yi++(DATABASE|SCHEMA)\\b~i",$Vh)){restart_session();set_session("dbs",null);stop_session();}if(!$_POST["only_errors"])echo"<p class='message' title='".h(connection()->info)."'>".lang(171,$ma)."$Oj\n";}echo($Sk?"<div id='$Tk' class='hidden'>\n$Sk</div>\n":"");if($Vc){echo"<div id='$Wc' class='hidden explain'>\n";print_select_result($Vc,$f,$Ug);echo"</div>\n";}}$fj=microtime(true);}while(connection()->next_result());}$I=substr($I,$Ag);$Ag=0;if($Cb){$ac=driver()->delimiter;$Cb=false;}}}}}if($Dc)echo"<p class='message'>".lang(172)."\n";else{$te=connection()->inTransaction();driver()->rollback();if($te)echo"<pre><code class='jush-".JUSH."'>ROLLBACK -- Adminer</code></pre>\n";if($_POST["only_errors"])echo"<p class='message'>".lang(173,$ob-count($Kc))," <span class='time'>(".format_time($Zj).")</span>\n";elseif($Kc&&$ob>1)echo"<p class='error'>".lang(168).": ".implode("",$Kc)."\n";}}else
echo"<p class='error'>".upload_error($I)."\n";}echo'
<form action="" method="post" enctype="multipart/form-data" id="form"';$xk="";if(!isset($_GET["import"]))echo
on('submit','sqlSubmit',remove_from_uri("sql|limit|error_stops|only_errors|history"));else
echo
on_upload_progress($xk);echo'>
';$Sc="<input type='submit' value='".lang(174)."' title='Ctrl+Enter'>";if(!isset($_GET["import"])){$Vh=$_GET["sql"];if($_POST)$Vh=$_POST["query"];elseif($_GET["history"]=="all")$Vh=$fe;elseif($_GET["history"]!="")$Vh=idx($fe[$_GET["history"]],0);echo"<p>";textarea("query",$Vh,20);echo($_POST?"":script("qs('textarea').focus();")),"<p>";adminer()->sqlPrintAfter();echo"$Sc\n",lang(175).": <input type='number' name='limit' class='size' value='".h($_POST?$_POST["limit"]:$_GET["limit"])."'>\n";}else{$Qd=(extension_loaded("zlib")?"[.gz]":"");echo"<fieldset><legend>".lang(176)."</legend><div>",($xk?input_hidden(ini_get("session.upload_progress.name"),$xk):""),"SQL$Qd: ".file_input(" name='sql_file[]' multiple","\n$Sc"),($xk?" <progress class='jsonly hidden' max='1' value='0'></progress>":""),"</div></fieldset>\n";$qe=adminer()->importServerPath();if($qe)echo"<fieldset><legend>".lang(177)."</legend><div>",lang(178,"<code>".h($qe)."$Qd</code>")," <input type='submit' name='webfile' value='".lang(179)."'>","</div></fieldset>\n";adminer()->importPrint();echo"<p>";}echo
checkbox("error_stops",1,($_POST?$_POST["error_stops"]:isset($_GET["import"])||$_GET["error_stops"]),lang(180))."\n",checkbox("only_errors",1,($_POST?$_POST["only_errors"]:isset($_GET["import"])||$_GET["only_errors"]),lang(181))."\n",input_token();if(!isset($_GET["import"])&&$fe){print_fieldset("history",lang(182),$_GET["history"]!="");for($X=end($fe);$X;$X=prev($fe)){$w=key($fe);list($Vh,$Oj,$_c)=$X;echo'<div><a href="'.h(ME."sql=&history=$w").'" class="hover">'.lang(13)."</a>"." <span class='time' title='".@date('Y-m-d',$Oj)."'>".@date("H:i:s",$Oj)."</span>"." <code class='jush-".JUSH."'>".shorten_utf8(preg_replace('~\s+~',' ',ltrim(preg_replace("~^(?:$rf).*~m",'',$Vh))),80,"</code>").($_c?" <span class='time'>($_c)</span>":"")."</div>\n";}echo"<input type='submit' name='clear' value='".lang(183)."'>\n","<a href='".h(ME."sql=&history=all")."'>".lang(184)."</a>\n","</div></fieldset>\n";}echo'</form>
';}elseif(isset($_GET["edit"])){$a=$_GET["edit"];$m=fields($a);$Z=(isset($_GET["select"])?($_POST["check"]&&count($_POST["check"])==1?where_check($_POST["check"][0],$m):""):where($_GET,$m));$vk=(isset($_GET["select"])?$_POST["edit"]:$Z);foreach($m
as$C=>$l){if((!$vk&&!isset($l["privileges"]["insert"]))||adminer()->fieldName($l)=="")unset($m[$C]);}if($_POST&&!$k&&!isset($_GET["select"])){$_=relative_uri((string)$_POST["referer"]);if($_POST["insert"])$_=($vk?null:relative_uri());elseif(!preg_match('~^.+&select=.+$~',$_))$_=ME."select=".url_escape($a);$v=indexes($a);$pk=unique_array($_GET["where"],$v);$Yh="\nWHERE $Z";if(isset($_POST["delete"]))queries_redirect($_,lang(185),driver()->delete($a,$Yh,$pk?0:1));else{$P=array();foreach($m
as$C=>$l){$X=process_input($l);if($X!==false&&$X!==null)$P[idf_escape($C)]=$X;}if($vk){if(!$P)redirect($_);queries_redirect($_,lang(186),driver()->update($a,$P,$Yh,$pk?0:1));if(is_ajax()){page_headers();page_messages($k);exit;}}else{$J=driver()->insert($a,$P);$hf=($J?last_id($J):0);queries_redirect($_,lang(187,($hf?" $hf":"")),$J);}}}$L=null;$I="";$Oj="";if($Z){$N=array();$Fi=array("*");foreach($m
as$C=>$l){if(isset($l["privileges"]["select"])){$xa=($_POST["clone"]&&$l["auto_increment"]?"''":convert_field($l));$c=($xa?"$xa AS ":"").idf_escape($C);$N[]=$c;if($xa)$Fi[]=$c;}}$L=array();if(!support("table")){$N=array("*");$Fi=$N;}if($N){$fj=microtime(true);$J=driver()->select($a,$N,array($Z),$N,array(),(isset($_GET["select"])?2:1));$I=str_replace("SELECT ".implode(", ",$N),"SELECT ".implode(", ",$Fi),driver()->query);$Oj=format_time($fj);if(!$J)$k=adminer()->error();else{$L=$J->fetch_assoc();if(!$L)$L=false;}if(isset($_GET["select"])&&(!$L||$J->fetch_assoc()))$L=null;}}if(!$m&&driver()->primary!=""){if(!$Z){$J=driver()->select($a,array("*"),array(),array("*"));$L=($J?$J->fetch_assoc():false);if(!$L)$L=array(driver()->primary=>"");}if($L){foreach($L
as$w=>$X){if(!$Z)$L[$w]=null;$m[$w]=array("field"=>$w,"null"=>($w!=driver()->primary),"auto_increment"=>($w==driver()->primary));}}}if($_POST["save"]){$Gh=array();foreach((array)$_POST["fields"]as$w=>$X)$Gh[bracket_escape($w,true)]=$X;$L=$Gh+($L?$L:array());}edit_form($a,$m,$L,$vk,$k,$I,$Oj);}elseif(isset($_GET["create"])){function
referencable_primary($Hi){$K=array();foreach(table_status('',true)as$vj=>$R){if($vj!=$Hi&&fk_support($R)){foreach(fields($vj)as$l){if($l["primary"]){if($K[$vj]){unset($K[$vj]);break;}$K[$vj]=$l;}}}}return$K;}$a=$_GET["create"];$mh=driver()->partitionBy;$qh=($mh&&$a!=""?driver()->partitionsInfo($a):array());$ei=referencable_primary($a);$xd=array();foreach($ei
as$vj=>$l)$xd[str_replace("`","``",$vj)."`".str_replace("`","``",$l["field"])]=$vj;$Xg=array();$S=array();if($a!=""){$Xg=fields($a);$S=table_status1($a);if(count($S)<2)$k=lang(12);}$L=$_POST;$L["fields"]=(array)$L["fields"];if($L["auto_increment_col"])$L["fields"][$L["auto_increment_col"]]["auto_increment"]=true;if($_POST&&!$k)save_settings(array("comments"=>$_POST["comments"],"defaults"=>$_POST["defaults"]));if($_POST&&!process_fields($L["fields"])&&!$k){if($_POST["drop"])queries_redirect(substr(ME,0,-1),lang(188),drop_tables(array($a)));else{$m=array();$pa=array();$Ak=false;$vd=array();$Wg=reset($Xg);$oa=" FIRST";foreach($L["fields"]as$w=>$l){$o=$xd[$l["type"]];$kk=($o!==null?$ei[$o]:$l);if($l["field"]!=""){if(!$l["generated"])$l["default"]=null;$Th=process_field($l,$kk);$pa[]=array($l["orig"],$Th,$oa);if(!$Wg||$Th!==process_field($Wg,$Wg)){$m[]=array($l["orig"],$Th,$oa);if($l["orig"]!=""||$oa)$Ak=true;}if($o!==null)$vd[idf_escape($l["field"])]=($a!=""&&JUSH!="sqlite"?"ADD":" ").format_foreign_key(array('table'=>$xd[$l["type"]],'source'=>array($l["field"]),'target'=>array($kk["field"]),'on_delete'=>$l["on_delete"],));$oa=" AFTER ".idf_escape($l["field"]);}elseif($l["orig"]!=""){$Ak=true;$m[]=array($l["orig"]);}if($l["orig"]!=""){$Wg=next($Xg);if(!$Wg)$oa="";}}$oh=array();if(in_array($L["partition_by"],$mh)){foreach($L
as$w=>$X){if(preg_match('~^partition~',$w))$oh[$w]=$X;}foreach($oh["partition_names"]as$w=>$C){if($C==""){unset($oh["partition_names"][$w]);unset($oh["partition_values"][$w]);}}$oh["partition_names"]=array_values($oh["partition_names"]);$oh["partition_values"]=array_values($oh["partition_values"]);if($oh==$qh)$oh=array();}elseif(preg_match("~partitioned~",$S["Create_options"]))$oh=null;$B=lang(189);if($a==""){cookie("adminer_engine",$L["Engine"]);$B=lang(190);}$C=trim($L["name"]);$_=ME.(support("table")?"table=":"select=").url_escape($C);$J=alter_table($a,$C,(JUSH=="sqlite"&&($Ak||$vd)?$pa:$m),$vd,($L["Comment"]!=$S["Comment"]?$L["Comment"]:null),($L["Engine"]&&$L["Engine"]!=$S["Engine"]?$L["Engine"]:""),($L["Collation"]&&$L["Collation"]!=$S["Collation"]?$L["Collation"]:""),($L["Auto_increment"]!=""?number($L["Auto_increment"]):""),$oh);if($J&&!Queries::$queries&&$a!=""&&!$m&&!$vd)redirect($_);queries_redirect($_,$B,$J);}}page_header(($a!=""?lang(42):lang(75)),$k,array("table"=>$a),h($a));if(!$_POST){$mk=driver()->types();$L=array("Engine"=>$_COOKIE["adminer_engine"],"fields"=>array(array("field"=>"","type"=>(isset($mk["int"])?"int":(isset($mk["integer"])?"integer":"")),"on_update"=>"")),"partition_names"=>array(""),);if($a!=""){$L=$S;$L["name"]=$a;$L["fields"]=array();if(!$_GET["auto_increment"])$L["Auto_increment"]="";foreach($Xg
as$l){if($l["generated"])$l["default"]=ltrim($l["default"]);$l["generated"]=$l["generated"]?:(isset($l["default"])?"DEFAULT":"");$L["fields"][]=$l;}if($mh){$L+=$qh;$L["partition_names"][]="";$L["partition_values"][]="";}}}$lb=flat_collations();$Fc=driver()->engines();foreach($Fc
as$Ec){if(!strcasecmp($Ec,$L["Engine"])){$L["Engine"]=$Ec;break;}}$Cf=max_input_vars(12,20);if($Cf){$ee=(count($L["fields"])>$Cf?"":" hidden");echo"<p".($ee?" id='max-fields' data-columns='$Cf'":"")." class='error$ee'>".max_input_vars_error()."\n";}echo'
<form action="" method="post" id="form">
<p>
';if(support("columns")||$a==""){echo
lang(191).": <input name='name'".($a==""&&!$_POST?" autofocus":"")." data-maxlength='64' value='".h($L["name"])."' autocapitalize='off'>\n",($Fc?html_select("Engine",array(""=>"(".lang(192).")")+$Fc,$L["Engine"],on('change','helpClose').on_help_value())."\n":"");if($lb)echo"<datalist id='collations'>".optionlist($lb)."</datalist>\n",(preg_match("~sqlite|mssql~",JUSH)?"":"<input list='collations' name='Collation' value='".h($L["Collation"])."' placeholder='(".lang(117).")'>\n");echo"<input type='submit' value='".lang(17)."'>\n";}if(support("columns")){echo"<div class='scrollable'>\n","<table id='edit-fields' class='nowrap'>\n";edit_fields($L["fields"],$lb,"TABLE",$xd);echo"</table>\n",script("editFields();"),"</div>\n<p>\n",lang(49).": <input type='number' name='Auto_increment' class='size' value='".h($L["Auto_increment"])."'>\n",checkbox("defaults",1,($_POST?$_POST["defaults"]:get_setting("defaults")),lang(193),on('click','columnShowClick',5),"jsonly");$rb=($_POST?$_POST["comments"]:get_setting("comments"));if(support("comment")){echo
checkbox("comments",1,$rb,lang(48),on('click','editingCommentsClick',true),"jsonly").' ';$b=" name='Comment' data-maxlength='".(min_version(5.5)?2048:60)."'".($rb?"":" class='hidden'");echo
adminer()->commentInput('TABLE',$b,$L["Comment"]);}echo'<p>
<input type=\'submit\' value=\'',lang(17),'\'>
';}echo'
';if($a!="")echo'<input type=\'submit\' name=\'drop\' value=\'',lang(142),'\'',confirm(lang(194,$a)),'>
';if($mh&&(JUSH=='sql'||$a=="")){$nh=preg_match('~RANGE|LIST~',$L["partition_by"]);print_fieldset("partition",lang(195),$L["partition_by"]);echo"<p>".html_select("partition_by",array_merge(array(""),$mh),$L["partition_by"],on('change','partitionByChange').on_help_value('.','PARTITION BY $&'))."\n","(<input name='partition' value='".h($L["partition"])."'>)\n",lang(196).": <input type='number' name='partitions' class='size".($nh||!$L["partition_by"]?" hidden":"")."' value='".h($L["partitions"])."'>\n","<table id='partition-table'".($nh?"":" class='hidden'").">\n","<thead><tr><th>".lang(197)."<th>".lang(198)."<tbody>\n";foreach($L["partition_names"]as$w=>$X)echo'<tr>','<td><input name="partition_names[]" value="'.h($X).'" autocapitalize="off"'.($w==count($L["partition_names"])-1?on('input','partitionNameChange'):'').'>','<td><input name="partition_values[]" value="'.h(idx($L["partition_values"],$w)).'">';echo"</table>\n</div></fieldset>\n";}echo
input_token(),'</form>
';}elseif(isset($_GET["indexes"])){$a=$_GET["indexes"];$ye=array("PRIMARY","UNIQUE","INDEX");$S=table_status1($a,true);$we=driver()->indexAlgorithms($S);if(preg_match('~MyISAM|M?aria'.(min_version(5.6,'10.0.5')?'|InnoDB':'').'~i',$S["Engine"]))$ye[]="FULLTEXT";if(preg_match('~MyISAM|M?aria'.(min_version(5.7,'10.2.2')?'|InnoDB':'').'~i',$S["Engine"]))$ye[]="SPATIAL";if(min_version('',11.7)&&preg_match('~MyISAM|InnoDB~i',$S["Engine"]))$ye[]="VECTOR";$v=indexes($a);$m=fields($a);$Nh=array();if(JUSH=="mongo"){$Nh=$v["_id_"];unset($ye[0]);unset($v["_id_"]);}$L=$_POST;if($L)save_settings(array("index_options"=>$L["options"]));if($_POST&&!$k&&!$_POST["add"]&&!$_POST["drop_col"]){$ra=array();foreach($L["indexes"]as$u){$C=$u["name"];if(in_array($u["type"],$ye)){$d=array();$of=array();$dc=array();$Kg=array();$xe=(support("partial_indexes")?$u["partial"]:"");$ve=(in_array($u["algorithm"],$we)?$u["algorithm"]:"");$P=array();ksort($u["columns"]);foreach($u["columns"]as$w=>$c){if($c!=""){$x=idx($u["lengths"],$w);$bc=idx($u["descs"],$w);$Jg=idx($u["opclasses"],$w);$P[]=($m[$c]?idf_escape($c):$c).($x?"(".(+$x).")":"").($Jg!=""?" ".idf_escape($Jg):"").($bc?" DESC":"");$d[]=$c;$of[]=($x?:null);$dc[]=$bc;$Kg[]="$Jg";}}$Tc=$v[$C];if($Tc){ksort($Tc["columns"]);ksort($Tc["lengths"]);ksort($Tc["descs"]);if($u["type"]==$Tc["type"]&&array_values($Tc["columns"])===$d&&(!$Tc["lengths"]||array_values($Tc["lengths"])===$of)&&array_values($Tc["descs"])===$dc&&(!$Tc["opclasses"]||array_values($Tc["opclasses"])===$Kg)&&$Tc["partial"]==$xe&&(!$we||$Tc["algorithm"]==$ve)){unset($v[$C]);continue;}}if($d)$ra[]=array($u["type"],$C,$P,$ve,$xe);}}foreach($v
as$C=>$Tc)$ra[]=array($Tc["type"],$C,"DROP");if(!$ra)redirect(ME."table=".url_escape($a));queries_redirect(ME."table=".url_escape($a),lang(199),alter_indexes($a,$ra));}page_header(lang(149),$k,array("table"=>$a),h($a));$jd=array_keys($m);if($_POST["add"]){foreach($L["indexes"]as$w=>$u){if($u["columns"][count($u["columns"])]!="")$L["indexes"][$w]["columns"][]="";}$u=end($L["indexes"]);if($u["type"]||array_filter($u["columns"],'strlen'))$L["indexes"][]=array("columns"=>array(1=>""));}if(!$L){foreach($v
as$w=>$u){$v[$w]["name"]=$w;$v[$w]["columns"][]="";}$v[]=array("columns"=>array(1=>""));$L["indexes"]=$v;}$of=(JUSH=="sql"||JUSH=="mssql");$Kg=driver()->indexOpclasses();$Pi=($_POST?$_POST["options"]:get_setting("index_options"));echo'
<form action="" method="post">
<div class="scrollable">
<table class="nowrap odds">
<thead><tr>
<th id="label-type">',lang(200);$oe=" class='idxopts".($Pi?"":" hidden")."'";if($we)echo"<th id='label-algorithm'$oe>".lang(201).doc_link(array('sql'=>'create-index.html#create-index-storage-engine-index-types','mariadb'=>'storage-engine-index-types/',));echo'<th><input type="submit" hidden>',lang(202).($of?"<span$oe> (".lang(203).")</span>":"");if($of||support("descidx"))echo
checkbox("options",1,$Pi,lang(123),on('click','indexOptionsShow'),"jsonly")."\n";echo'<th id="label-name">',lang(204);if(support("partial_indexes"))echo"<th id='label-condition'$oe>".lang(205);echo'<th><noscript>',icon("plus","add[0]","+",lang(124)),'</noscript>
<tbody>
';if($Nh){echo"<tr><td>PRIMARY<td>";foreach($Nh["columns"]as$w=>$c)echo
select_input(" disabled",array_combine($jd,$jd),$c),"<label><input disabled type='checkbox'>".lang(57)."</label> ";echo"<td><td>\n";}$Ue=1;foreach($L["indexes"]as$u){if(!$_POST["drop_col"]||$Ue!=key($_POST["drop_col"])){echo"<tr><td>".html_select("indexes[$Ue][type]",array(-1=>"")+$ye,$u["type"],($Ue==count($L["indexes"])?on('change','indexesAddRow'):""),"label-type");if($we)echo"<td$oe>".html_select("indexes[$Ue][algorithm]",array_merge(array(""),$we),$u['algorithm'],"","label-algorithm");echo"<td>";ksort($u["columns"]);$r=1;foreach($u["columns"]as$w=>$c){echo"<span>".select_input(" name='indexes[$Ue][columns][$r]' title='".lang(46)."'".on('change','indexesChangeColumn',(JUSH=="sql"?"":$_GET["indexes"]."_")),($m&&($c==""||$m[$c])?array_combine($jd,$jd):array()),$c)," <span$oe>",($of?"<input type='number' name='indexes[$Ue][lengths][$r]' class='size' value='".h(idx($u["lengths"],$w))."' title='".lang(122)."'>":"");if($Kg){$Jg=idx($u["opclasses"],$w);echo
html_select("indexes[$Ue][opclasses][$r]",array(""=>"(".lang(206).")")+array_combine($Kg,$Kg)+($Jg!=""?array($Jg=>$Jg):array()),$Jg),'';}echo(support("descidx")?checkbox("indexes[$Ue][descs][$r]",1,idx($u["descs"],$w),lang(57)):""),"<br>","</span></span>";$r++;}echo"<td><input name='indexes[$Ue][name]' value='".h($u["name"])."' autocapitalize='off' aria-labelledby='label-name'>\n";if(support("partial_indexes"))echo"<td$oe><input name='indexes[$Ue][partial]' value='".h($u["partial"])."' autocapitalize='off' aria-labelledby='label-condition'>\n";echo"<td>".icon("cross","drop_col[$Ue]","x",lang(126),on('click','editingRemoveRow','indexes$1[type]'));}$Ue++;}echo'</table>
</div>
<p>
<input type=\'submit\' value=\'',lang(17),'\'>
',input_token(),'</form>
';}elseif(isset($_GET["database"])){$L=$_POST;if($_POST&&!$k&&!$_POST["add"]){$C=trim($L["name"]);if($_POST["drop"]){$_GET["db"]="";queries_redirect(remove_from_uri("db|database"),lang(207),drop_databases(array(DB)));}elseif($C!==DB){if(DB!=""){$_GET["db"]=$C;queries_redirect(preg_replace('~\bdb=[^&]*&~','',ME)."db=".url_escape($C),lang(208),rename_database($C,(string)$L["collation"]));}else{$h=explode("\n",str_replace("\r","",$C));$mj=true;$ff="";foreach($h
as$i){if(count($h)==1||$i!=""){if(!create_database($i,(string)$L["collation"]))$mj=false;$ff=$i;}}restart_session();set_session("dbs",null);queries_redirect(preg_replace('~&db=[^&]*~','',ME)."db=".url_escape($ff),lang(209),$mj);}}else{if(!$L["collation"])redirect(substr(ME,0,-1));query_redirect("ALTER DATABASE ".idf_escape($C).(preg_match('~^[a-z0-9_]+$~i',$L["collation"])?" COLLATE $L[collation]":""),substr(ME,0,-1),lang(210));}}page_header(DB!=""?lang(65):lang(130),$k,array(),h(DB));$lb=collations();$C=DB;if($_POST)$C=$L["name"];elseif(DB!="")$L["collation"]=db_collation(DB,$lb);elseif(JUSH=="sql"){foreach(get_vals("SHOW GRANTS")as$Hd){if(preg_match('~ ON (`(([^\\\\`]|``|\\\\.)*)%`\.\*)?~',$Hd,$A)&&$A[1]){$C=stripcslashes(idf_unescape("`$A[2]`"));break;}}}echo'
<form action="" method="post">
<p>
',($_POST["add"]||strpos($C,"\n")?'<textarea autofocus name="name" rows="10" cols="40">'.h($C).'</textarea><br>':'<input name="name" autofocus value="'.h($C).'" data-maxlength="64" autocapitalize="off">')."\n",($lb?html_select("collation",array(""=>"(".lang(117).")")+$lb,$L["collation"]).doc_link(array('sql'=>"charset-charsets.html",'mariadb'=>"supported-character-sets-and-collations/",)):"")."\n",'<input type=\'submit\' value=\'',lang(17),'\'>
';if(DB!="")echo"<input type='submit' name='drop' value='".lang(142)."'".confirm(lang(194,DB)).">\n";elseif(!$_POST["add"]&&$_GET["db"]=="")echo
icon("plus","add[0]","+",lang(124))."\n";echo
input_token(),'</form>
';}elseif(isset($_GET["call"])){$ca=($_GET["name"]?:$_GET["call"]);page_header(lang(211).": ".h($ca),$k);$vi=(isset($_GET["callf"])?"FUNCTION":"PROCEDURE");$ti=routine($_GET["call"],$vi);$re=array();$ch=array();foreach($ti["fields"]as$r=>$l){if(substr($l["inout"],-3)=="OUT"&&JUSH=='sql')$ch[$r]="@".idf_escape($l["field"])." AS ".idf_escape($l["field"]);if(!$l["inout"]||substr($l["inout"],0,2)=="IN")$re[]=$r;}if(!$k&&$_POST){$Ua=array();foreach($ti["fields"]as$w=>$l){$X="";if(in_array($w,$re)){$X=process_input($l);if($X===false)$X="''";if(isset($ch[$w]))connection()->query("SET @".idf_escape($l["field"])." = $X");}if(isset($ch[$w]))$Ua[]="@".idf_escape($l["field"]);elseif(in_array($w,$re))$Ua[]=$X;}$I=(isset($_GET["callf"])?"SELECT ":"CALL ").(idx($ti["returns"],"type")=="record"?"* FROM ":"").table($ca)."(".implode(", ",$Ua).")";$fj=microtime(true);$J=connection()->multi_query($I);$ma=connection()->affected_rows;echo
adminer()->selectQuery($I,$fj,!$J);if(!$J)echo"<p class='error'>".adminer()->error()."\n";else{$f=connect();if($f)$f->select_db(DB);do{$J=connection()->store_result();if(is_object($J))print_select_result($J,$f);else
echo"<p class='message'>".lang(212,$ma)." <span class='time'>".@date("H:i:s")."</span>\n";}while(connection()->next_result());if($ch)print_select_result(connection()->query("SELECT ".implode(", ",$ch)));}}echo'
<form action="" method="post">
';if($re){echo"<table class='layout'>\n";foreach($re
as$w){$l=$ti["fields"][$w];$C=$l["field"];echo"<tr><th>".adminer()->fieldName($l);$Y=idx($_POST["fields"],$C);if($Y!=""){if($l["type"]=="set")$Y=implode(",",$Y);}input($l,$Y,idx($_POST["function"],$C,""));echo"\n";}echo"</table>\n";}echo'<p>
<input type=\'submit\' value=\'',lang(211),'\'>
',input_token(),'</form>

',adminer()->commentValue($vi,$ti['comment']);}elseif(isset($_GET["foreign"])){$a=$_GET["foreign"];$C=$_GET["name"];$L=$_POST;if($_POST&&!$k&&!$_POST["add"]&&!$_POST["change"]&&!$_POST["change-js"]){if(!$_POST["drop"]){$L["source"]=array_filter($L["source"],'strlen');ksort($L["source"]);$Fj=array();foreach($L["source"]as$w=>$X)$Fj[$w]=$L["target"][$w];$L["target"]=$Fj;}if(JUSH=="sqlite")$J=recreate_table($a,$a,array(),array(),array(" $C"=>($L["drop"]?"":" ".format_foreign_key($L))));else{$ra="ALTER TABLE ".table($a);$J=($C==""||queries("$ra DROP ".(JUSH=="sql"?"FOREIGN KEY ":"CONSTRAINT ").idf_escape($C)));if(!$L["drop"])$J=queries("$ra ADD".format_foreign_key($L));}queries_redirect(ME."table=".url_escape($a),($L["drop"]?lang(213):($C!=""?lang(214):lang(215))),$J);if(!$L["drop"])$k=lang(216);}page_header(($C!=""?lang(217):lang(154)),$k,array("table"=>$a),h($C!=""?$C:$a));if($_POST){ksort($L["source"]);if($_POST["change"]||$_POST["change-js"])$L["target"]=array();else$L["source"][]="";}elseif($C!=""){$xd=foreign_keys($a);$L=$xd[$C];$L["source"][]="";}else{$L["table"]=$a;$L["source"]=array("");}echo'
<form action="" method="post">
';$Wi=array_keys(fields($a));if($L["db"]!="")connection()->select_db($L["db"]);if($L["ns"]!=""){$Yg=get_schema();set_schema($L["ns"]);}$di=array_keys(array_filter(table_status('',true),'Adminer\fk_support'));$Fj=array_keys(fields(in_array($L["table"],$di)?$L["table"]:reset($di)));$b=on('change','foreignChange');echo"<p><label>".lang(218).": ".html_select("table",$di,$L["table"],$b)."</label>\n";if(JUSH!="sqlite"){$Tb=array();foreach(adminer()->databases()as$i){if(!information_schema($i))$Tb[]=$i;}echo"<label>".lang(76).": ".html_select("db",$Tb,$L["db"]!=""?$L["db"]:$_GET["db"],$b)."</label>";}echo
input_hidden("change-js"),'<noscript><p><input type=\'submit\' name=\'change\' value=\'',lang(219),'\'></noscript>
<table>
<thead><tr><th id="label-source">',lang(151),'<th id="label-target">',lang(152),'<tbody>
';$Ue=0;foreach($L["source"]as$w=>$X){echo"<tr>","<td>".html_select("source[".(+$w)."]",array(-1=>"")+$Wi,$X,($Ue==count($L["source"])-1?on('change','foreignAddRow'):""),"label-source"),"<td>".html_select("target[".(+$w)."]",$Fj,idx($L["target"],$w),"","label-target");$Ue++;}echo'</table>
<p>
<label>',lang(119),': ',html_select("on_delete",array(-1=>"")+explode("|",driver()->onActions),$L["on_delete"]),'</label>
<label>',lang(118),': ',html_select("on_update",array(-1=>"")+explode("|",driver()->onActions),$L["on_update"]),'</label>
',(support("deferrable")?html_select("deferrable",array('NOT DEFERRABLE','DEFERRABLE','DEFERRABLE INITIALLY DEFERRED'),$L["deferrable"]).' ':''),doc_link(array('sql'=>"innodb-foreign-key-constraints.html",'mariadb'=>"foreign-keys/",)),'<p>
<input type=\'submit\' value=\'',lang(17),'\'>
<noscript><p><input type=\'submit\' name=\'add\' value=\'',lang(220),'\'></noscript>
';if($C!="")echo'<input type=\'submit\' name=\'drop\' value=\'',lang(142),'\'',confirm(lang(194,$C)),'>
';echo
input_token(),'</form>
';}elseif(isset($_GET["view"])){$a=$_GET["view"];$L=$_POST;$Zg="VIEW";if(JUSH=="pgsql"&&$a!=""){$hj=table_status1($a);$Zg=strtoupper($hj["Engine"]);}if($_POST&&!$k){$C=trim($L["name"]);$xa=" AS\n$L[select]";$_=ME."table=".url_escape($C);$B=lang(221);$U=($_POST["materialized"]?"MATERIALIZED VIEW":"VIEW");if(!$_POST["drop"]&&$a==$C&&JUSH!="sqlite"&&$U=="VIEW"&&$Zg=="VIEW")query_redirect((JUSH=="mssql"?"ALTER":"CREATE OR REPLACE")." VIEW ".table($C).$xa,$_,$B);else{$Jj="adminer_".uniqid();drop_create("DROP $Zg ".table($a),"CREATE $U ".table($C).$xa,"DROP $U ".table($C),"CREATE $U ".table($Jj).$xa,"DROP $U ".table($Jj),($_POST["drop"]?substr(ME,0,-1):$_),lang(222),$B,lang(223),$a,$C);}}if(!$_POST&&$a!=""){$L=view($a);$L["name"]=$a;$L["materialized"]=($Zg!="VIEW");if(!$k)$k=adminer()->error();}page_header(($a!=""?lang(41):lang(224)),$k,array("table"=>$a),h($a));echo'
<form action="" method="post">
<p>',lang(204),': <input name="name" value="',h($L["name"]),'" data-maxlength="64" autocapitalize="off">
',(support("materializedview")?" ".checkbox("materialized",1,$L["materialized"],lang(145)):""),'<p>';textarea("select",$L["select"]);echo'<p>
<input type=\'submit\' value=\'',lang(17),'\'>
';if($a!="")echo'<input type=\'submit\' name=\'drop\' value=\'',lang(142),'\'',confirm(lang(194,$a)),'>
';echo
input_token(),'</form>
';}elseif(isset($_GET["event"])){$aa=$_GET["event"];$Ie=array("YEAR","QUARTER","MONTH","DAY","HOUR","MINUTE","WEEK","SECOND","YEAR_MONTH","DAY_HOUR","DAY_MINUTE","DAY_SECOND","HOUR_MINUTE","HOUR_SECOND","MINUTE_SECOND");$ij=array("ENABLED"=>"ENABLE","DISABLED"=>"DISABLE","SLAVESIDE_DISABLED"=>"DISABLE ON SLAVE");$L=$_POST;if($_POST&&!$k){if($_POST["drop"])query_redirect("DROP EVENT ".idf_escape($aa),substr(ME,0,-1),lang(225));elseif(in_array($L["INTERVAL_FIELD"],$Ie)&&isset($ij[$L["STATUS"]])){$yi="\nON SCHEDULE ".($L["INTERVAL_VALUE"]?"EVERY ".q($L["INTERVAL_VALUE"])." $L[INTERVAL_FIELD]".($L["STARTS"]?" STARTS ".q($L["STARTS"]):"").($L["ENDS"]?" ENDS ".q($L["ENDS"]):""):"AT ".q($L["STARTS"]))." ON COMPLETION".($L["ON_COMPLETION"]?"":" NOT")." PRESERVE";queries_redirect(substr(ME,0,-1),($aa!=""?lang(226):lang(227)),queries(($aa!=""?"ALTER EVENT ".idf_escape($aa).$yi.($aa!=$L["EVENT_NAME"]?"\nRENAME TO ".idf_escape($L["EVENT_NAME"]):""):"CREATE EVENT ".idf_escape($L["EVENT_NAME"]).$yi)."\n".$ij[$L["STATUS"]]." COMMENT ".q($L["EVENT_COMMENT"]).rtrim(" DO\n$L[EVENT_DEFINITION]",";").";"));}}page_header(($aa!=""?lang(228).": ".h($aa):lang(229)),$k);if(!$L&&$aa!=""){$M=get_rows("SELECT * FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ".q(DB)." AND EVENT_NAME = ".q($aa));$L=reset($M);}echo'
<form action="" method="post">
<table class="layout">
<tr><th>',lang(204),'<td><input name="EVENT_NAME" value="',h($L["EVENT_NAME"]),'" data-maxlength="64" autocapitalize="off">
<tr><th title="datetime">',lang(230),'<td><input name="STARTS" value="',h("$L[EXECUTE_AT]$L[STARTS]"),'">
<tr><th title="datetime">',lang(231),'<td><input name="ENDS" value="',h($L["ENDS"]),'">
<tr><th>',lang(232),'<td><input type="number" name="INTERVAL_VALUE" value="',h($L["INTERVAL_VALUE"]),'" class="size"> ',html_select("INTERVAL_FIELD",$Ie,$L["INTERVAL_FIELD"]),'<tr><th>',lang(133),'<td>',html_select("STATUS",$ij,$L["STATUS"]),'<tr><th>',lang(48),'<td><input name="EVENT_COMMENT" value="',h($L["EVENT_COMMENT"]),'" data-maxlength="64">
<tr><th><td>',checkbox("ON_COMPLETION","PRESERVE",$L["ON_COMPLETION"]=="PRESERVE",lang(233)),'</table>
<p>';textarea("EVENT_DEFINITION",$L["EVENT_DEFINITION"]);echo'<p>
<input type=\'submit\' value=\'',lang(17),'\'>
';if($aa!="")echo'<input type=\'submit\' name=\'drop\' value=\'',lang(142),'\'',confirm(lang(194,$aa)),'>
';echo
input_token(),'</form>
';}elseif(isset($_GET["procedure"])){$ca=($_GET["name"]?:$_GET["procedure"]);$ti=(isset($_GET["function"])?"FUNCTION":"PROCEDURE");$L=$_POST;$L["fields"]=(array)$L["fields"];if($_POST&&!process_fields($L["fields"])&&!$k){foreach($L["fields"]as$w=>$l){if($l["field"]=="")unset($L["fields"][$w]);}$Dg=routine_id($ca,routine($_GET["procedure"],$ti));$og=routine_id($L["name"],$L);$g=create_routine($ti,$L);$_=substr(ME,0,-1);$B=lang(234);if(!$_POST["drop"]&&$Dg==$og&&connection()->flavor!="mysql")query_redirect(substr_replace($g,' OR REPLACE',6,0),$_,$B);else{$Jj="adminer_".uniqid();drop_create("DROP $ti $Dg",$g,"DROP $ti $og",create_routine($ti,array("name"=>$Jj)+$L),"DROP $ti ".routine_id($Jj,$L),$_,lang(235),$B,lang(236),$ca,$L["name"]);}}page_header(($ca!=""?(isset($_GET["function"])?lang(237):lang(238)).": ".h($ca):(isset($_GET["function"])?lang(239):lang(240))),$k);if(!$_POST){if($ca=="")$L["language"]="sql";else{$L=routine($_GET["procedure"],$ti);$L["name"]=$ca;}}$lb=(JUSH=="sql"?flat_collations():array());$ui=routine_languages();echo($lb?"<datalist id='collations'>".optionlist($lb)."</datalist>":""),'
<form action="" method="post" id="form">
<p>',lang(204),': <input name="name" value="',h($L["name"]),'" data-maxlength="64" autocapitalize="off">
',($ui?"<label>".lang(23).": ".html_select("language",$ui,$L["language"])."</label>\n":""),'<input type=\'submit\' value=\'',lang(17),'\'>
<div class="scrollable">
<table id="edit-fields" class="nowrap">
';edit_fields($L["fields"],$lb,$ti);if(isset($_GET["function"])){echo"<tr><td>".lang(241);edit_type("returns",(array)$L["returns"],$lb,array(),(JUSH=="pgsql"?array("void","trigger"):array()));}echo'</table>
',script("editFields();"),'</div>
<p>';textarea("definition",$L["definition"],20);echo'<p>
<input type=\'submit\' value=\'',lang(17),'\'>
';if($ca!="")echo'<input type=\'submit\' name=\'drop\' value=\'',lang(142),'\'',confirm(lang(194,$ca)),'>
';echo
input_token(),'</form>
';}elseif(isset($_GET["check"])){$a=$_GET["check"];$C=$_GET["name"];$L=$_POST;if($L&&!$k){if(JUSH=="sqlite")$J=recreate_table($a,$a,array(),array(),array(),"",array(),"$C",($L["drop"]?"":$L["clause"]));else{$J=($C==""||queries("ALTER TABLE ".table($a)." DROP CONSTRAINT ".idf_escape($C)));if(!$L["drop"])$J=queries("ALTER TABLE ".table($a)." ADD".($L["name"]!=""?" CONSTRAINT ".idf_escape($L["name"]):"")." CHECK ($L[clause])");}queries_redirect(ME."table=".url_escape($a),($L["drop"]?lang(242):($C!=""?lang(243):lang(244))),$J);}page_header(($C!=""?lang(245):lang(156)),$k,array("table"=>$a),h($C!=""?$C:$a));if(!$L){$cb=driver()->checkConstraints($a);$L=array("name"=>$C,"clause"=>$cb[$C]);}echo'
<form action="" method="post">
<p>';if(JUSH!="sqlite")echo
lang(204).': <input name="name" value="'.h($L["name"]).'" data-maxlength="64" autocapitalize="off"> ';echo
doc_link(array('sql'=>"create-table-check-constraints.html",'mariadb'=>"constraint/",),"?"),'<p>';textarea("clause",$L["clause"]);echo'<p><input type=\'submit\' value=\'',lang(17),'\'>
';if($C!="")echo'<input type=\'submit\' name=\'drop\' value=\'',lang(142),'\'',confirm(lang(194,$C)),'>
';echo
input_token(),'</form>
';}elseif(isset($_GET["trigger"])){$a=$_GET["trigger"];$C="$_GET[name]";$gk=trigger_options();$L=(array)trigger($C,$a)+array("Trigger"=>$a."_bi");if($_POST){if(!$k&&in_array($_POST["Timing"],$gk["Timing"])&&in_array($_POST["Event"],$gk["Event"])&&in_array($_POST["Type"],$gk["Type"])){$Gg=" ON ".table($a);$tc="DROP TRIGGER ".idf_escape($C).(JUSH=="pgsql"?$Gg:"");$_=ME."table=".url_escape($a);if($_POST["drop"])query_redirect($tc,$_,lang(246));else{if($C!="")queries($tc);queries_redirect($_,($C!=""?lang(247):lang(248)),queries(create_trigger($Gg,$_POST)));if($C!="")queries(create_trigger($Gg,$L+array("Type"=>reset($gk["Type"]))));}}$L=$_POST;}page_header(($C!=""?lang(249):lang(158)),$k,array("table"=>$a),h($C!=""?$C:$a));$fk=on('change','triggerChange',"^".preg_quote($a,"/")."_[ba][iud]$",$a);echo'
<form action="" method="post" id="form">
<table class="layout">
<tr><th>',lang(250),'<td>',html_select("Timing",$gk["Timing"],$L["Timing"],$fk),'<tr><th>',lang(251),'<td>',html_select("Event",$gk["Event"],$L["Event"],$fk),(in_array("UPDATE OF",$gk["Event"])?" <input name='Of' value='".h($L["Of"])."' class='hidden'>":""),'<tr><th>',lang(47),'<td>',html_select("Type",$gk["Type"],$L["Type"]),'<tr><th>',lang(204),'<td><input name="Trigger" value="',h($L["Trigger"]),'" data-maxlength="64" autocapitalize="off">
</table>
',script("fire(qs('#form')['Timing'], 'change');"),'<p>';textarea("Statement",$L["Statement"]);echo'<p>
<input type=\'submit\' value=\'',lang(17),'\'>
';if($C!="")echo'<input type=\'submit\' name=\'drop\' value=\'',lang(142),'\'',confirm(lang(194,$C)),'>
';echo
input_token(),'</form>
';}elseif(isset($_GET["user"])){function
grant($Hd,array$Rh,$d,$Gg){if(!$Rh)return
true;if($Rh==array("ALL PRIVILEGES","GRANT OPTION"))return($Hd=="GRANT"?queries("$Hd ALL PRIVILEGES$Gg WITH GRANT OPTION"):queries("$Hd ALL PRIVILEGES$Gg")&&queries("$Hd GRANT OPTION$Gg"));return
queries("$Hd ".preg_replace('~(GRANT OPTION)\([^)]*\)~','\1',implode("$d, ",$Rh).$d).$Gg);}$ea=$_GET["user"];$Rh=array(""=>array("All privileges"=>""));foreach(get_rows("SHOW PRIVILEGES")as$L){foreach(explode(",",($L["Privilege"]=="Grant option"?"":$L["Context"]))as$zb)$Rh[$zb=="File access on server"?"Server Admin":$zb][$L["Privilege"]]=$L["Comment"];}unset($Rh["Server Admin"]["Usage"]);foreach($Rh["Tables"]as$w=>$X)unset($Rh["Databases"][$w]);$ng=array();if($_POST){foreach($_POST["objects"]as$w=>$X)$ng[$X]=(array)$ng[$X]+idx($_POST["grants"],$w,array());}$Id=array();if(isset($_GET["host"])&&($J=connection()->query("SHOW GRANTS FOR ".q($ea)."@".q($_GET["host"])))){while($L=$J->fetch_row()){if(preg_match('~GRANT (.*) ON (.*) TO ~',$L[0],$A)&&preg_match_all('~ *([^(,]*[^ ,(])( *\([^)]+\))?~',$A[1],$_f,PREG_SET_ORDER)){foreach($_f
as$X){if($X[1]!="USAGE")$Id["$A[2]$X[2]"][$X[1]]=true;if(preg_match('~ WITH GRANT OPTION~',$L[0]))$Id["$A[2]$X[2]"]["GRANT OPTION"]=true;}}}}if($_POST&&!$k){$Fg=(isset($_GET["host"])?q($ea)."@".q($_GET["host"]):"''");if($_POST["drop"])query_redirect("DROP USER $Fg",ME."privileges=",lang(252));else{$qg=q($_POST["user"])."@".q($_POST["host"]);$rh=$_POST["pass"];$Fb=false;$J=true;if($Fg!=$qg){$Fb=queries("CREATE USER $qg IDENTIFIED BY ".($_POST["hashed"]?"PASSWORD ":"").q($rh));$J=$Fb;}elseif($rh!="")$J=queries("SET PASSWORD FOR $qg = ".(min_version(8,99)||$_POST["hashed"]?q($rh):"PASSWORD(".q($rh).")"));if($J){$pi=array();foreach($ng
as$wg=>$Hd){if(isset($_GET["grant"]))$Hd=array_filter($Hd);$Hd=array_keys($Hd);if(isset($_GET["grant"]))$pi=array_diff(array_keys(array_filter($ng[$wg],'strlen')),$Hd);elseif($Fg==$qg){$Cg=array_keys((array)$Id[$wg]);$pi=array_diff($Cg,$Hd);$Hd=array_diff($Hd,$Cg);unset($Id[$wg]);}if(preg_match('~^(.+)\s*(\(.*\))?$~U',$wg,$A)&&(!grant("REVOKE",$pi,$A[2]," ON $A[1] FROM $qg")||!grant("GRANT",$Hd,$A[2]," ON $A[1] TO $qg"))){$J=false;break;}}}if($J&&isset($_GET["host"])){if($Fg!=$qg)queries("DROP USER $Fg");elseif(!isset($_GET["grant"])){foreach($Id
as$wg=>$pi){if(preg_match('~^(.+)(\(.*\))?$~U',$wg,$A))grant("REVOKE",array_keys($pi),$A[2]," ON $A[1] FROM $qg");}}}if($J&&!Queries::$queries)redirect(ME."privileges=");queries_redirect(ME."privileges=",(isset($_GET["host"])?lang(253):lang(254)),$J);if($Fb)connection()->query("DROP USER $qg");}}page_header((isset($_GET["host"])?lang(31).": ".h("$ea@$_GET[host]"):lang(166)),$k,array("privileges"=>array('',lang(69))));$L=$_POST;if($L)$Id=$ng;else{$L=$_GET+array("host"=>get_val("SELECT SUBSTRING_INDEX(CURRENT_USER, '@', -1)"));$Id[(DB==""||$Id?"":idf_escape(addcslashes(DB,"%_\\"))).".*"]=array();}echo'<form action="" method="post">
<table class="layout">
<tr><th>',lang(29),'<td><input name="host" data-maxlength="60" value="',h($L["host"]),'" autocapitalize="off">
<tr><th>',lang(31),'<td><input name="user" data-maxlength="80" value="',h($L["user"]),'" autocapitalize="off">
<tr><th>',lang(32),'<td><input name="pass" id="pass" value="',h($L["pass"]),'" autocomplete="new-password">
',($L["hashed"]?"":script("typePassword(qs('#pass'));")),(min_version(8,99)?"":checkbox("hashed",1,$L["hashed"],lang(255),on('click','hashedClick'))),'</table>

',"<table class='odds'>\n","<thead><tr><th colspan='2'>".lang(69).doc_link(array('sql'=>"grant.html#priv_level"));$r=0;foreach($Id
as$wg=>$Hd){echo'<th>'.($wg!="*.*"?"<input name='objects[$r]' value='".h($wg)."' size='10' autocapitalize='off'>":input_hidden("objects[$r]","*.*")."*.*");$r++;}echo"<tbody>\n";foreach(array(""=>"","Server Admin"=>lang(29),"Databases"=>lang(33),"Tables"=>lang(147),"Procedures"=>lang(256),)as$zb=>$bc){foreach((array)$Rh[$zb]as$Qh=>$pb){echo"<tr><td".($bc?">$bc<td":" colspan='2'").' lang="en" title="'.h($pb).'">'.h($Qh);$r=0;foreach($Id
as$wg=>$Hd){$C="'grants[$r][".h(strtoupper($Qh))."]'";$Y=$Hd[strtoupper($Qh)];if($zb=="Server Admin"&&$wg!=(isset($Id["*.*"])?"*.*":".*"))echo"<td>";elseif(isset($_GET["grant"]))echo"<td><select name=$C><option><option value='1'".($Y?" selected":"").">".lang(257)."<option value='0'".($Y=="0"?" selected":"").">".lang(258)."</select>";else
echo"<td align='center'><label class='block'>","<input type='checkbox' name=$C value='1'".($Y?" checked":"").($Qh=="All privileges"?" id='grants-$r-all'":($Qh=="Grant option"?"":on('click','grantsClick',"grants-$r-all"))).">","</label>";$r++;}}}echo"</table>\n",'<p>
<input type=\'submit\' value=\'',lang(17),'\'>
';if(isset($_GET["host"]))echo'<input type=\'submit\' name=\'drop\' value=\'',lang(142),'\'',confirm(lang(194,"$ea@$_GET[host]")),'>
';echo
input_token(),'</form>
';}elseif(isset($_GET["processlist"])){if(support("kill")){if($_POST&&!$k){$af=0;foreach((array)$_POST["kill"]as$X){if(adminer()->killProcess($X))$af++;}queries_redirect(ME."processlist=",lang(259,$af),$af||!$_POST["kill"]);}}page_header(lang(131),$k);echo'
<form action="" method="post">
<div class="scrollable">
<table class="nowrap checkable odds"',on('click','tableClick').on('dblclick','tableClick'),'>
';$r=-1;foreach(adminer()->processList()as$r=>$L){if(!$r){echo"<thead><tr lang='en'>".(support("kill")?"<td class='hover'>":"");foreach($L
as$w=>$X)echo"<th>$w".doc_link(array('sql'=>"show-processlist.html#processlist_".strtolower($w),));echo"<tbody>\n";}echo"<tr>".(support("kill")?"<td class='hover'>".checkbox("kill[]",$L[JUSH=="sql"?"Id":"pid"],0):"");foreach($L
as$w=>$X)echo"<td>".($X!=""&&((JUSH=="sql"&&$w=="Info"&&preg_match("~Query|Killed~",$L["Command"]))||(JUSH=="pgsql"&&$w=="query")||(JUSH=="oracle"&&$w=="sql_text"))?"<code class='jush-".JUSH."' data-full='".h($X)."'>".shorten_utf8($X,100,"</code>").' <a href="'.h(($L["db"]!=""?preg_replace('~&db=[^&]*~','',ME)."db=".url_escape($L["db"])."&":ME)."sql=".url_escape($X)).'">'.lang(260).'</a>'.' '.copy_icon():h($X));echo"\n";}echo'</table>
</div>
<p>
',script("copyCode(qsl('table'));");if(support("kill"))echo($r+1)."/".lang(261,max_connections()),"<p><input type='submit' value='".lang(262)."'>\n";echo
input_token(),'</form>
',script("tableCheck();");}elseif($_GET["select"]!=""){$a=$_GET["select"];$S=table_status1($a);$v=indexes($a);$m=fields($a);$xd=column_foreign_keys($a);$Bg=$S["Oid"];$la=get_settings("adminer_import");$ri=array();$d=array();$Ci=array();$Rg=array();$Mj=null;foreach($m
as$w=>$l){$C=adminer()->fieldName($l);$kg=html_entity_decode(strip_tags($C),ENT_QUOTES);if(isset($l["privileges"]["select"])&&$C!=""){$d[$w]=$kg;if(is_shortable($l))$Mj=adminer()->selectLengthProcess();}if(isset($l["privileges"]["where"])&&$C!="")$Ci[$w]=$kg;if(isset($l["privileges"]["order"])&&$C!="")$Rg[$w]=$kg;$ri+=$l["privileges"];}list($N,$Kd)=adminer()->selectColumnsProcess($d,$v);$N=array_unique($N);$Kd=array_unique($Kd);$Oe=count($Kd)<count($N);$Z=adminer()->selectSearchProcess($m,$v);$E=adminer()->selectOrderProcess($m,$v);$y=adminer()->selectLimitProcess();if($_GET["val"]&&is_ajax()){header("Content-Type: text/plain; charset=utf-8");foreach($_GET["val"]as$qk=>$L){$xa=convert_field($m[key($L)]);$N=array($xa?:idf_escape(key($L)));$Z[]=where_check(bracket_escape($qk,true),$m);$K=driver()->select($a,$N,$Z,$N);if($K)echo
first($K->fetch_row());}exit;}$Nh=$sk=array();foreach($v
as$u){if($u["type"]=="PRIMARY"){$Nh=array_flip($u["columns"]);$sk=($N?$Nh:array());foreach($sk
as$w=>$X){if(in_array(idf_escape($w),$N))unset($sk[$w]);}break;}}if($Bg&&!$Nh){$Nh=$sk=array($Bg=>0);$v[]=array("type"=>"PRIMARY","columns"=>array($Bg));}if($_POST&&!$k){$Vk=$Z;if(!$_POST["all"]&&is_array($_POST["check"])){$cb=array();foreach($_POST["check"]as$Za)$cb[]=where_check($Za,$m);$Vk[]="((".implode(") OR (",$cb)."))";}$Xk=$Vk;$Vk=($Vk?"\nWHERE ".implode(" AND ",$Vk):"");if($_POST["export"]){save_settings(array("output"=>$_POST["output"],"format"=>$_POST["format"]),"adminer_import");dump_headers($a);adminer()->dumpTable($a,"");$Ei=($N?:array("*"));$Ab=convert_fields($d,$m,$N);if($Ab)$Ei[]=substr($Ab,2);$I="";if(is_array($_POST["check"])&&!$Nh){$Ad=implode(", ",$Ei)."\nFROM ".table($a);$Md=($Kd&&$Oe?"\nGROUP BY ".implode(", ",$Kd):"").($E?"\nORDER BY ".implode(", ",$E):"");$ok=array();foreach($_POST["check"]as$X)$ok[]="(SELECT".limit($Ad,"\nWHERE ".($Z?implode(" AND ",$Z)." AND ":"").where_check($X,$m).$Md,1).")";$I=implode(" UNION ALL ",$ok);}adminer()->dumpData($a,"table",$I,$Ei,$Xk,($Oe?$Kd:array()),$E);adminer()->dumpFooter();exit;}if(!adminer()->selectEmailProcess($Z,$xd)){if($_POST["save"]||$_POST["delete"]){$J=true;$ma=0;$La=false;$P=array();if(!$_POST["delete"]){foreach($m
as$C=>$X){$t=bracket_escape($C);if(isset($_POST["fields"][$t])||$_FILES["fields-$t"]){$X=process_input($m[$C]);if($X!==null&&($_POST["clone"]||$X!==false))$P[idf_escape($C)]=($X!==false?$X:idf_escape($C));}}}if($_POST["delete"]||$P){$I=($_POST["clone"]?"INTO ".table($a)." (".implode(", ",array_keys($P)).")\nSELECT ".implode(", ",$P)."\nFROM ".table($a):"");if($_POST["all"]||($Nh&&is_array($_POST["check"]))||$Oe){$J=($_POST["delete"]?driver()->delete($a,$Vk):($_POST["clone"]?queries("INSERT $I$Vk".driver()->insertReturning($a)):driver()->update($a,$P,$Vk)));$ma=connection()->affected_rows;if(is_object($J))$ma+=$J->num_rows;}else{$La=count((array)$_POST["check"])>1&&driver()->begin();foreach((array)$_POST["check"]as$X){$Uk="\nWHERE ".($Z?implode(" AND ",$Z)." AND ":"").where_check($X,$m);$J=($_POST["delete"]?driver()->delete($a,$Uk,1):($_POST["clone"]?queries("INSERT".limit1($a,$I,$Uk)):driver()->update($a,$P,$Uk,1)));if(!$J)break;$ma+=connection()->affected_rows;}if($La&&$J&&!driver()->commit())$J=false;}}$B=lang(263,$ma);if($_POST["clone"]&&$J&&$ma==1){$hf=last_id($J);if($hf)$B=lang(187," $hf");}queries_redirect(remove_from_uri($_POST["all"]&&$_POST["delete"]?"page|next":""),$B,$J);if($La)driver()->rollback();if(!$_POST["delete"]){$Gh=(array)$_POST["fields"];edit_form($a,array_intersect_key($m,$Gh),$Gh,!$_POST["clone"],$k);page_footer();exit;}}elseif(!$_POST["import"]){$J=true;$ma=0;$La=count((array)$_POST["val"])>1&&driver()->begin();foreach((array)$_POST["val"]as$qk=>$L){$P=array();foreach($L
as$w=>$X){$w=bracket_escape($w,true);$P[idf_escape($w)]=(preg_match('~char|text~',$m[$w]["type"])||$X!=""?adminer()->processInput($m[$w],$X):"NULL");}$J=driver()->update($a,$P," WHERE ".($Z?implode(" AND ",$Z)." AND ":"").where_check(bracket_escape($qk,true),$m),($Oe||$Nh?0:1)," ");if(!$J)break;$ma+=connection()->affected_rows;}if($La)$J=$J&&driver()->commit();queries_redirect(remove_from_uri(),lang(263,$ma),$J);if($La)driver()->rollback();}elseif(!is_string($kd=get_file("csv_file",true)))$k=upload_error($kd);elseif(!preg_match('~~u',$kd))$k=lang(264);else{save_settings(array("output"=>$la["output"],"format"=>$_POST["separator"]),"adminer_import");$mb=array_keys($m);$Ji=($_POST["separator"]=="csv"?",":($_POST["separator"]=="tsv"?"\t":";"));$Jb=parse_csv($kd,$Ji);$ma=count($Jb);driver()->begin();$M=array();foreach($Jb
as$w=>$Kk){if(!$w&&!array_diff($Kk,$mb)){$mb=$Kk;$ma--;}else{$P=array();foreach($Kk
as$r=>$ib)$P[idf_escape($mb[$r])]=($ib==""&&$m[$mb[$r]]["null"]?"NULL":q(csv_value($ib)));$M[]=$P;}}$J=(!$M||driver()->insertUpdate($a,$M,$Nh));if($J)driver()->commit();queries_redirect(remove_from_uri("page|next"),lang(265,$ma),$J);driver()->rollback();}}}$vj=adminer()->tableName($S);if(is_ajax()){page_headers();ob_start();}else
page_header(lang(51).": $vj",$k);$P=null;if(isset($ri["insert"])||!support("table")){$P="";foreach((array)$_GET["where"]as$X){$Y=$X["val"];if(is_array($Y))$Y=(count($Y)==1&&preg_match('~^val-(.*)~s',reset($Y),$A)?$A[1]:"");if($X["col"]!=""&&$Y!=""&&($X["op"]=="="||(!$X["op"]&&(is_array($X["val"])||!preg_match('~[_%]~',$Y)))))$P
.="&set[".url_escape(bracket_escape($X["col"]))."]=".url_escape($Y);}}adminer()->selectLinks($S,$P);if(!$d&&support("table"))echo"<p class='error'>".lang(266).($m?".":": ".adminer()->error())."\n";else{echo"<form action='' id='form'>\n","<div hidden>";hidden_fields_get();echo(DB!=""?input_hidden("db",DB).(isset($_GET["ns"])?input_hidden("ns",$_GET["ns"]):""):""),input_hidden("select",$a),"</div>\n";adminer()->selectColumnsPrint($N,$d);adminer()->selectSearchPrint($Z,$Ci,$v);adminer()->selectOrderPrint($E,$Rg,$v);adminer()->selectLimitPrint($y);if($Mj!==null)adminer()->selectLengthPrint($Mj);adminer()->selectActionPrint($v);echo"</form>\n";foreach((array)$_GET["where"]as$X){if($X["op"]=="SQL"&&!in_array($_SERVER["HTTP_SEC_FETCH_SITE"],array("","same-origin"))){echo"<p class='error'>".lang(112).' '.lang(113)."\n";page_footer();exit;}}$F=$_GET["page"];$_d=null;if($F=="last"){$_d=get_val(count_rows($a,$Z,$Oe,$Kd));$F=floor(max(0,intval($_d)-1)/$y);}$Di=$N;$Ld=$Kd;if(!$Di){$Di[]="*";$Ab=convert_fields($d,$m,$N);if($Ab)$Di[]=substr($Ab,2);}foreach($N
as$w=>$X){$l=$m[idf_unescape($X)];if($l&&($xa=convert_field($l)))$Di[$w]="$xa AS $X";}if(JUSH=="pgsql"||JUSH=="mssql"){foreach((array)$_GET["columns"]as$w=>$X){if(isset($Di[$w])&&$X["fun"])$Di[$w].=" AS ".idf_escape(apply_sql_function($X["fun"],($X["col"]!=""?$X["col"]:"*")));}}if(!$Oe&&$sk){foreach($sk
as$w=>$X){$Di[]=idf_escape($w);if($Ld)$Ld[]=idf_escape($w);}}$J=driver()->select($a,$Di,$Z,$Ld,$E,$y,$F,true);if(!is_object($J))echo"<p class='error'>".(adminer()->error()?:lang(25))."\n";else{if(JUSH=="mssql"&&$F)$J->seek($y*$F);$Cc=array();$M=array();while($L=$J->fetch_assoc()){if($F&&JUSH=="oracle")unset($L["RNUM"]);$M[]=$L;}$Wd=($y&&(support("cursor")?$_GET["next"]!="":count($M)>=$y));if(is_ajax()&&$Wd)header("X-Next-Page: ".pagination_href($F+1));if($_GET["modify"]&&$M){$If=max_input_vars(count($M[0])+1,20);echo($If&&count($M)>$If?"<p class='error'>".max_input_vars_error()."\n":"");}echo"<form action='' method='post' enctype='multipart/form-data'".on_upload_progress($xk).">\n";if($_GET["page"]!="last"&&$y&&$Kd&&$Oe&&JUSH=="sql")$_d=get_val(" SELECT FOUND_ROWS()");if(!$M)echo"<p class='message'>".lang(15)."\n";else{$Ha=adminer()->backwardKeys($a,$vj);echo"<div class='scrollable'>","<table id='table' class='nowrap checkable odds'".on('click','tableClick').on('dblclick','tableClick').on('keydown','editingKeydown').">\n","<thead><tr>".(!$Kd&&$N?"":"<td class='hover check'><input type='checkbox' id='all-page' class='jsonly' title='".lang(267)."'".on('click','formCheck','^check').">");$lg=array();$Ed=array();reset($N);$ai=1;foreach($M[0]as$w=>$X){if(!isset($sk[$w])){$X=idx($_GET["columns"],key($N))?:array();$l=$m[$N?($X?$X["col"]:current($N)):$w];$C=($l?adminer()->fieldName($l,$ai):($X["fun"]?"*":h($w)));if($C!=""){$ai++;$lg[$w]=$C;$c=idf_escape($w);$je=remove_from_uri('(order|desc)[^=]*|page|next').'&order[0]='.url_escape($w);$bc="&desc[0]=1";$Ti=preg_replace('~ DESC( NULLS LAST)?$~','',$E[0]);$Vi=($Ti==$c||$Ti==$w);echo"<th id='th[".h(bracket_escape($w))."]'".($Vi?" aria-sort='".($Ti==$E[0]?"ascending":"descending")."'":"").">";$Dd=apply_sql_function($X["fun"],$C);$Ui=isset($l["privileges"]["order"])||$Dd!=$C;echo($Ui?"<a href='".h($je.($Vi&&$Ti==$E[0]?$bc:''))."'>$Dd</a>":$Dd);$Qf=($Ui?"<a href='".h($je.$bc)."' title='".lang(57)."' class='text'> ↓</a>":'');if(!$X["fun"]&&isset($l["privileges"]["where"]))$Qf
.="<a href='#fieldset-search' title='".lang(54)."' class='text jsonly'".on('click','selectSearch',$w)."> =</a>";echo($Qf?"<span class='column'>$Qf</span>":"");}$Ed[$w]=$X["fun"];next($N);}}$of=array();if($_GET["modify"]){foreach($M
as$L){foreach($L
as$w=>$X)$of[$w]=max($of[$w],min(40,strlen(utf8_decode($X))));}}echo($Ha?"<th>".lang(268):"")."<tbody>\n";if(is_ajax())ob_end_clean();foreach(adminer()->rowDescriptions($M,$xd)as$ig=>$L){$pk=unique_array($M[$ig],$v);if(!$pk){$pk=array();reset($N);foreach($M[$ig]as$w=>$X){if(!preg_match('~^(COUNT|AVG|GROUP_CONCAT|MAX|MIN|SUM)\(~',current($N)))$pk[$w]=$X;next($N);}}$qk="";foreach($pk
as$w=>$X){$l=(array)$m[$w];$Ne=is_blob($l);if((JUSH=="sql"||JUSH=="pgsql")&&($Ne||preg_match('~'.text_type().'~',$l["type"]))&&strlen($X)>64){$w=(strpos($w,'(')?$w:idf_escape($w));$w="MD5(".($Ne||JUSH!='sql'||preg_match("~^utf8~",$l["collation"])?$w:"CONVERT($w USING ".charset(connection()).")").")";$X=md5($Ne?(string)driver()->value($X,$l):$X);}$qk
.="&".($X!==null?"where[".url_escape(bracket_escape($w))."]=".url_escape($X===false?"f":$X):"null[]=".url_escape($w));}echo"<tr>".(!$Kd&&$N?"":"<td class='hover check'>".($Oe||information_schema(DB)?"":"<a href='".h(ME."edit=".url_escape($a).$qk)."' class='edit'>".lang(269)."</a> ").checkbox("check[]",substr($qk,1),in_array(substr($qk,1),(array)$_POST["check"])));reset($N);foreach($L
as$w=>$X){if(isset($lg[$w])){$c=current($N);$l=(array)$m[$w];if($X!=""&&(!isset($Cc[$w])||$Cc[$w]!=""))$Cc[$w]=(is_mail($X)?$lg[$w]:"");$z="";if(is_blob($l)&&$X!="")$z=ME.'download='.url_escape($a).'&field='.url_escape($w).$qk;if(!$z&&$X!==null){foreach((array)$xd[$w]as$o){if(count($xd[$w])==1||end($o["source"])==$w){$z="";foreach($o["source"]as$r=>$Wi)$z
.=where_link($r,$o["target"][$r],$M[$ig][$Wi]);$z=($o["db"]!=""?preg_replace('~([?&]db=)[^&]+~','\1'.url_escape($o["db"]),ME):ME).'select='.url_escape($o["table"]).$z;if($o["ns"])$z=preg_replace('~([?&]ns=)[^&]+~','\1'.url_escape($o["ns"]),$z);if(count($o["source"])==1)break;}}}if($c=="COUNT(*)"){$z=ME."select=".url_escape($a);$r=0;foreach((array)$_GET["where"]as$W){if(!array_key_exists($W["col"],$pk))$z
.=where_link($r++,$W["col"],$W["val"],$W["op"]);}foreach($pk
as$Xe=>$W)$z
.=where_link($r++,$Xe,$W);}$ke=select_value($X,$z,$l,$Mj);$t=bracket_escape($qk);$s=h("val[$t][".bracket_escape($w)."]");$Ih=idx(idx($_POST["val"],$t),bracket_escape($w));$vk=idx($l["privileges"],"update");$zc=!is_array($L[$w])&&!is_blob($l)&&is_utf8($X)&&$M[$ig][$w]==$X&&!$Ed[$w]&&!$l["generated"]&&$vk;$U=(preg_match('~^(AVG|MIN|MAX)\((.+)\)~',$c,$A)?$m[idf_unescape($A[2])]["type"]:$l["type"]);$Lj=preg_match('~text|json|lob~',$U);$Pe=preg_match(number_type(),$U)||preg_match('~^(CHAR_LENGTH|ROUND|FLOOR|CEIL|TIME_TO_SEC|COUNT|SUM)\(~',$c);echo"<td id='$s'".($Pe&&($X===null||is_numeric(strip_tags($ke))||$U=="money")?" class='number'":"");if(($_GET["modify"]&&$zc&&$X!==null)||$Ih!==null){$Rd=h($Ih!==null?$Ih:$X);echo">".($Lj?"<textarea name='$s' cols='30' rows='".(substr_count($X,"\n")+1)."'>$Rd</textarea>":"<input name='$s' value='$Rd' size='$of[$w]'>");}else{$xf=strpos($ke,"<i>…</i>");echo($vk?" data-text='".($xf?2:($Lj?1:0))."'".($zc?"":" data-warning='".lang(270)."'"):"").">$ke";}}next($N);}if($Ha)echo"<td>";adminer()->backwardKeysPrint($Ha,$M[$ig]);echo"</tr>\n";}if(is_ajax())exit;echo"</table>\n","</div>\n";}if(!is_ajax()){if($M||$F||$Wd){$Rc=true;if($_GET["page"]!="last"){if(!$y||(count($M)<$y&&($M||!$F)))$_d=($F?$F*$y:0)+count($M);elseif(JUSH!="sql"||!$Oe){$_d=($Oe?false:found_rows($S,$Z));if(intval($_d)<max(1e4,2*($F+1)*$y))$_d=first(slow_query(count_rows($a,$Z,$Oe,$Kd)));elseif(JUSH=='sql'||JUSH=='pgsql')$Rc=false;}}if(!support("cursor"))$Wd=(($_d===false?count($M)+1:$_d-$F*$y)>$y);$fh=($y&&($Wd||$F));if($fh)echo($Wd?'<p><a href="'.h(pagination_href($F+1)).'" class="loadmore"'.on('click','selectLoadMore',lang(271)).'>'.lang(272).'</a>':''),"\n";echo"<div class='footer'><div>\n";if($fh){$Gf=($_d===false?$F+($M?(count($M)>=$y?2:1):0):floor(($_d-1)/$y));echo"<fieldset><legend>".lang(273)."</legend>";if(!support("cursor")){echo
pagination(0,$F).($F>5?" …":"");for($r=max(1,$F-4);$r<min($Gf,$F+5);$r++)echo
pagination($r,$F);if($Gf>0)echo($F+5<$Gf?" …":""),($Rc&&$_d!==false?pagination($Gf,$F):" <a href='".h(remove_from_uri("page")."&page=last")."' title='~$Gf'>".lang(274)."</a>");}else
echo
pagination(0,$F).($F>1?" …":""),($F?pagination($F,$F):""),($Wd?pagination($F+1,$F)." …":"");echo"</fieldset>\n";}echo"<fieldset>","<legend>".lang(275)."</legend>";$jc=($Rc?"":"~ ").$_d;$bf=($_d!==false?($Rc?"":"~ ").lang(170,$_d):"");echo
checkbox("all",1,0,$bf,on('click','countRows',$jc))."\n","</fieldset>\n";if(adminer()->selectCommandPrint())echo'<fieldset',($_GET["modify"]?'':" title='".lang(276)."'"),'>
<legend><a href=\'',h($_GET["modify"]?remove_from_uri("modify"):relative_uri()."&modify=1"),'\'>',lang(277),'</a></legend><div>
<input type=\'submit\' id=\'save\' value=\'',lang(17),'\'',($_GET["modify"]?'':" class='jsonly' disabled"),'>
</div></fieldset>

<fieldset><legend>',lang(141),' <span id="selected"></span></legend><div>
<input type=\'submit\' name=\'edit\' value=\'',lang(13),'\'>
<input type=\'submit\' name=\'clone\' value=\'',lang(260),'\'>
<input type=\'submit\' name=\'delete\' value=\'',lang(21),'\'',confirm(),'>
</div></fieldset>
';$yd=adminer()->dumpFormat();foreach((array)$_GET["columns"]as$c){if($c["fun"]){unset($yd['sql']);break;}}if($yd){print_fieldset("export",lang(74)." <span id='selected2'></span>");$dh=adminer()->dumpOutput();echo($dh?html_select("output",$dh,$la["output"])." ":""),html_select("format",$yd,$la["format"])," <input type='submit' name='export' value='".lang(74)."'>\n","</div></fieldset>\n";}adminer()->selectEmailPrint(array_filter($Cc,'strlen'),$d);echo"</div></div>\n";}if(adminer()->selectImportPrint())echo"<p>","<a href='#import' class='toggle'>".lang(73)."</a>","<span id='import'".($_POST["import"]?"":" class='hidden'").">: ",($xk?input_hidden(ini_get("session.upload_progress.name"),$xk):""),file_input(" name='csv_file'"," ".html_select("separator",array("csv"=>"CSV,","csv;"=>"CSV;","tsv"=>"TSV"),$la["format"])." <input type='submit' name='import' value='".lang(73)."'>".($xk?" <progress class='jsonly hidden' max='1' value='0'></progress>":"")),"</span>";echo
input_token(),"</form>\n",(!$Kd&&$N?"":script("tableCheck();"));}}}if(is_ajax()){ob_end_clean();exit;}}elseif(isset($_GET["variables"])){$hj=isset($_GET["status"]);page_header($hj?lang(133):lang(132));$Lk=($hj?adminer()->showStatus():adminer()->showVariables());if(!$Lk)echo"<p class='message'>".lang(15)."\n";else{echo"<table>\n";foreach($Lk
as$L){echo"<tr>";$w=array_shift($L);echo"<th><code class='jush-".JUSH.($hj?"status":"set")."'>".h($w)."</code>";foreach($L
as$X)echo"<td>".nl_br(h($X));}echo"</table>\n";}}elseif(isset($_GET["script"])){header("Content-Type: application/json; charset=utf-8");if($_GET["script"]=="db"){$pj=array("Data_length"=>0,"Index_length"=>0,"Data_free"=>0);foreach(table_status()as$C=>$S){json_row("Comment-$C",h($S["Comment"]));if(!is_view($S)||preg_match('~materialized~i',$S["Engine"])){foreach(array("Engine","Collation")as$w)json_row("$w-$C",h($S[$w]));foreach(array_keys($pj+array("Auto_increment"=>0,"Rows"=>0))as$w){if(array_key_exists($w,$S))json_row("$w-$C",format_status($S,$w));if($S[$w]!=""&&isset($pj[$w]))$pj[$w]+=($S["Engine"]!="InnoDB"||$w!="Data_free"?$S[$w]:0);}}}if(function_exists('Adminer\db_status'))$pj=db_status();foreach($pj
as$w=>$X)json_row("sum-$w",format_number($X));json_row("");}elseif($_GET["script"]=="kill"){if(!$k)connection()->query("KILL ".number($_POST["kill"]));}else{foreach(count_tables(adminer()->databases(false))as$i=>$X){json_row("tables-$i",$X);json_row("size-$i",db_size($i));}json_row("");}exit;}else{if(!isset($_GET["select"])&&support("single_table")){$T=tables_list();if($T)redirect(ME.(support("table")?"table=":"select=").url_escape(key($T)));}$Nf=ME.(isset($_GET["select"])?"select=&":"");$Dj=array_merge((array)$_POST["tables"],(array)$_POST["views"]);if($Dj&&!$k&&!$_POST["search"]){$J=true;$B="";if(JUSH=="sql"&&$_POST["tables"]&&count($_POST["tables"])>1&&($_POST["drop"]||$_POST["truncate"]||$_POST["copy"]))queries("SET foreign_key_checks = 0");if($_POST["truncate"]){if($_POST["tables"])$J=truncate_tables($_POST["tables"]);$B=lang(278);}elseif($_POST["move"]){$J=move_tables((array)$_POST["tables"],(array)$_POST["views"],$_POST["target"]);$B=lang(279);}elseif($_POST["copy"]){$J=copy_tables((array)$_POST["tables"],(array)$_POST["views"],$_POST["target"]);$B=lang(280);}elseif($_POST["drop"]){if($_POST["views"])$J=drop_views($_POST["views"]);if($J&&$_POST["tables"])$J=drop_tables($_POST["tables"]);$B=lang(281);}elseif(JUSH=="sqlite"&&$_POST["check"]){foreach((array)$_POST["tables"]as$R){foreach(get_rows("PRAGMA integrity_check(".q($R).")")as$L)$B
.="<b>".h($R)."</b>: ".h($L["integrity_check"])."<br>";}}elseif(JUSH!="sql"){$J=(JUSH=="sqlite"?queries("VACUUM"):apply_queries("VACUUM".($_POST["optimize"]?" ANALYZE":""),(array)$_POST["tables"]));$B=lang(282);}elseif(!$_POST["tables"])$B=lang(12);elseif($J=queries(($_POST["optimize"]?"OPTIMIZE":($_POST["check"]?"CHECK":($_POST["repair"]?"REPAIR":"ANALYZE")))." TABLE ".implode(", ",array_map('Adminer\idf_escape',$_POST["tables"])))){while($L=$J->fetch_assoc())$B
.="<b>".h($L["Table"])."</b>: ".h($L["Msg_text"])."<br>";}queries_redirect(relative_uri(),$B,$J);}page_header(($_GET["ns"]==""?lang(33).": ".h(DB):lang(165).": ".h($_GET["ns"])),$k,true);if(adminer()->homepage()){if($_GET["ns"]!==""){$E=$_GET["order"];$Bd=($E||support("fast_status"));echo"<div>\n","<h3 id='tables-views'>".lang(283)."</h3>\n";$Cj=($Bd?table_status():tables_list());if(!$Cj)echo"<p class='message'>".lang(12)."\n";else{echo"<form action='' method='post'>\n";if(support("table")){echo"<fieldset><legend>".lang(284)." <span id='selected2'></span></legend><div>",html_select("op",adminer()->operators(),idx($_POST,"op",JUSH=="elastic"?"should":"LIKE %%"))," <input type='search' name='query' value='".h($_POST["query"])."'".on('keydown','submitKeydown','search').">"," <input type='submit' name='search' value='".lang(54)."'>\n","</div></fieldset>\n";if(!$k&&$_POST["search"]&&$_POST["query"]!=""){$_GET["where"][0]["op"]=$_POST["op"];search_tables();}}echo"<div class='scrollable'>\n","<table class='nowrap checkable odds'".on('click','tableClick').on('dblclick','tableClick').">\n",'<thead><tr class="wrap">','<td class="hover"><input id="check-all" type="checkbox" class="jsonly" title="'.lang(164).'"'.on('click','formCheck','^(tables|views)\[').'>','<th'.(!$E&&JUSH!='sqlite'?" aria-sort='ascending'":'').'><a href="'.h(substr($Nf,0,-1)).'">'.lang(147).'</a>';$d=array("Engine"=>array(lang(285).doc_link(array('sql'=>'storage-engines.html'))));if(collations())$d["Collation"]=array(lang(137).doc_link(array('sql'=>'charset-charsets.html','mariadb'=>'supported-character-sets-and-collations/')));if(function_exists('Adminer\alter_table'))$d["Data_length"]=array(lang(286).doc_link(array('sql'=>'show-table-status.html',)),"create",lang(42),);if(support("indexes"))$d["Index_length"]=array(lang(287).doc_link(array('sql'=>'show-table-status.html',)),"indexes",lang(150),);$d["Data_free"]=array(lang(288).doc_link(array('sql'=>'show-table-status.html')),"edit",lang(43));if(function_exists('Adminer\alter_table'))$d["Auto_increment"]=array(lang(49).doc_link(array('sql'=>'example-auto-increment.html','mariadb'=>'auto_increment/')),"auto_increment=1&create",lang(42),);$d["Rows"]=array(lang(289).doc_link(array('sql'=>'show-table-status.html',)),"select",lang(39),);if(support("comment"))$d["Comment"]=array(lang(48).doc_link(array('sql'=>'show-table-status.html',)));$ya=array('Engine','Collation','Comment');foreach($d
as$w=>$c)echo"<th".($E==$w?" aria-sort='".(in_array($w,$ya)?"ascending":"descending")."'":"")."><a href='".h($Nf)."order=$w'>$c[0]</a>";echo"<tbody>\n";if($E){uasort($Cj,function($ga,$Ea)use($E,$ya){$K=($ga[$E]<$Ea[$E]?-1:($ga[$E]>$Ea[$E]?1:0));return(in_array($E,$ya)?$K:-$K);});}$T=0;$pj=array("Data_length"=>0,"Index_length"=>0,"Data_free"=>0);foreach($Cj
as$C=>$hj){$Ok=($Bd?is_view($hj):$hj!==null&&!preg_match('~table|sequence~i',$hj));$hj=($Bd?$hj:array('Engine'=>$hj));$s=h("Table-".$C);echo'<tr><td class="hover">'.checkbox(($Ok?"views[]":"tables[]"),$C,in_array("$C",$Dj,true),"","","",$s),'<th>'.(support("table")||support("indexes")?"<a href='".h(ME)."table=".url_escape($C)."' title='".lang(40)."' id='$s'>".h($C).'</a>':h($C));if($Ok&&!preg_match('~materialized~i',$hj['Engine'])){$Rj=lang(146);echo'<td colspan="'.(count($d)-(support("comment")?2:1)).'">'.(support("view")?"<a href='".h(ME)."view=".url_escape($C)."' title='".lang(41)."'>$Rj</a>":$Rj),"<td align='right'><a href='".h(ME)."select=".url_escape($C)."' title='".lang(39)."'>?</a>";if(support("comment"))echo'<td>'.h($hj['Comment']);}else{if($Bd){foreach(array_keys($pj)as$w)$pj[$w]+=($hj["Engine"]!="InnoDB"||$w!="Data_free"?idx($hj,$w):0);}foreach($d
as$w=>$c){$s=" id='$w-".h($C)."'";echo($c[1]?"<td align='right'><a href='".h(ME."$c[1]=").url_escape($C)."'$s title='$c[2]'>".format_status($hj,$w)."</a>":"<td$s>".h(idx($hj,$w,'?')));}$T++;}echo"\n";}echo"<tr><td class='hover'><th>".lang(261,count($Cj)),"<td>".h(JUSH=="sql"?get_val("SELECT @@default_storage_engine"):""),(collations()?"<td>".h(db_collation(DB,collations())):'');if($Bd&&function_exists('Adminer\db_status'))$pj=db_status();foreach($pj
as$w=>$oj)echo($d[$w]?"<td align='right' id='sum-$w'>".($Bd?format_number($oj):""):"");echo"\n","</table>\n",($Bd?'':script("ajaxSetHtml('".js_escape(ME)."script=db');")),"</div>\n";if(!information_schema(DB)){$Hk="<input type='submit' value='".lang(290)."'".on_help("VACUUM")."> ";$Ng="<input type='submit' name='optimize' value='".lang(291)."'".on_help(JUSH=="sql"?"OPTIMIZE TABLE":"VACUUM ANALYZE")."> ";$Oh=(JUSH=="sqlite"?$Hk."<input type='submit' name='check' value='".lang(292)."'".on_help("PRAGMA integrity_check")."> ":(JUSH=="pgsql"?$Hk.$Ng:(JUSH=="sql"?"<input type='submit' value='".lang(293)."'".on_help("ANALYZE TABLE")."> ".$Ng."<input type='submit' name='check' value='".lang(292)."'".on_help("CHECK TABLE")."> "."<input type='submit' name='repair' value='".lang(294)."'".on_help("REPAIR TABLE")."> ":""))).(function_exists('Adminer\truncate_tables')?"<input type='submit' name='truncate' value='".lang(295)."'".confirm().on_help(JUSH=="sqlite"?"DELETE":"TRUNCATE".(JUSH=="pgsql"?"":" TABLE"))."> ":"").(function_exists('Adminer\drop_tables')?"<input type='submit' name='drop' value='".lang(142)."'".confirm().on_help("DROP TABLE").">":"");echo($Oh?"<div class='footer'><div>\n<fieldset><legend>".lang(141)." <span id='selected'></span></legend><div>$Oh\n</div></fieldset>\n":"");$h=(support("scheme")?adminer()->schemas():adminer()->databases());if(count($h)!=1&&function_exists('Adminer\move_tables')){echo"<fieldset><legend>".lang(296)." <span id='selected3'></span></legend><div>";$i=(isset($_POST["target"])?$_POST["target"]:(support("scheme")?$_GET["ns"]:DB));echo($h?html_select("target",$h,$i):'<input name="target" value="'.h($i).'" autocapitalize="off">'),"</label> <input type='submit' name='move' value='".lang(125)."'>",(support("copy")?" <input type='submit' name='copy' value='".lang(22)."'> ".checkbox("overwrite",1,$_POST["overwrite"],lang(297)):""),"</div></fieldset>\n";}echo"<input type='hidden' name='all' value=''".on('click','countTables',$T).">\n",input_token(),"</div></div>\n";}echo"</form>\n",script("tableCheck();");}echo(function_exists('Adminer\alter_table')?"<p class='links hover'><a href='".h(ME)."create='>".lang(75)."</a>\n":''),(support("view")?"<a href='".h(ME)."view='>".lang(224)."</a>\n":""),"</div>\n";if(support("routine")){echo"<div>\n","<h3 id='routines'>".lang(70)."</h3>\n";$wi=routines();if($wi){echo"<table class='odds'>\n",'<thead><tr><th>'.lang(204).'<td>'.lang(47).'<td>'.lang(241)."<td class='hover'><tbody>\n";foreach($wi
as$L){$C=($L["SPECIFIC_NAME"]==$L["ROUTINE_NAME"]?"":"&name=".url_escape($L["ROUTINE_NAME"]));echo'<tr>','<th><a href="'.h(ME.($L["ROUTINE_TYPE"]!="PROCEDURE"?'callf=':'call=').url_escape($L["SPECIFIC_NAME"]).$C).'" title="'.lang(211).'">'.h($L["ROUTINE_NAME"]).'</a>','<td>'.h($L["ROUTINE_TYPE"]),'<td>'.h($L["DTD_IDENTIFIER"]),'<td class="hover"><a href="'.h(ME.($L["ROUTINE_TYPE"]!="PROCEDURE"?'function=':'procedure=').url_escape($L["SPECIFIC_NAME"]).$C).'">'.lang(153)."</a>";}echo"</table>\n";}echo'<p class="links hover">'.(support("procedure")?'<a href="'.h(ME).'procedure=">'.lang(240).'</a>':'').'<a href="'.h(ME).'function=">'.lang(239)."</a>\n","</div>\n";}if(support("event")){echo"<div>\n","<h3 id='events'>".lang(72)."</h3>\n";$M=get_rows("SHOW EVENTS");if($M){echo"<table>\n","<thead><tr><th>".lang(204)."<td>".lang(298)."<td>".lang(230)."<td>".lang(231)."<td class='hover'><tbody>\n";foreach($M
as$L)echo"<tr>","<th>".h($L["Name"]),"<td>".($L["Execute at"]?lang(299)."<td>".h($L["Execute at"]):lang(232)." ".h($L["Interval value"])." ".h($L["Interval field"])."<td>".h($L["Starts"])),"<td>".h($L["Ends"]),'<td class="hover"><a href="'.h(ME).'event='.url_escape($L["Name"]).'">'.lang(153).'</a>';echo"</table>\n";$Oc=get_val("SELECT @@event_scheduler");if($Oc&&$Oc!="ON")echo"<p class='error'><code class='jush-sqlset'>event_scheduler</code>: ".h($Oc)."\n";}echo'<p class="links hover"><a href="'.h(ME).'event=">'.lang(229)."</a>\n","</div>\n";}}}}page_footer();