package executor

import (
	"testing"

	"github.com/naibabiji/wp-panel/database"
)

func TestForgetImageOptimizationFingerprintsLeavesFilesRetryable(t *testing.T) {
	openTestDB(t)
	result, err := database.GetDB().Exec(`INSERT INTO websites
		(name,domain,status,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path)
		VALUES('images','images.test','active','wordpress','wp_images','/tmp/images','','','','','')`)
	if err != nil {
		t.Fatal(err)
	}
	siteID, _ := result.LastInsertId()
	if _, err := database.GetDB().Exec(`INSERT INTO site_image_optimization_files
		(site_id,relative_path,original_size,optimized_size,mtime_unix) VALUES(?,?,?,?,?)`, siteID, "2026/photo.jpg", 100, 80, 123); err != nil {
		t.Fatal(err)
	}
	forgetImageOptimizationFingerprints(database.GetDB(), int(siteID), map[string]int64{"2026/photo.jpg": 80})
	candidate := imageBatchCandidate{RelativePath: "2026/photo.jpg", Size: 80, ModUnix: 123}
	if pending := filterAlreadyOptimizedImages(database.GetDB(), int(siteID), []imageBatchCandidate{candidate}); len(pending) != 1 {
		t.Fatalf("pending=%v", pending)
	}
}
