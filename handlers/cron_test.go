package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
)

func TestValidateCronInputRejectsSiteBoundCommandTask(t *testing.T) {
	siteID := 1
	msg := validateCronInput("site command", "0 1 * * *", "echo ok", "command", "", "", &siteID)
	if msg == "" {
		t.Fatal("validateCronInput accepted a command task with site_id, want rejection")
	}
}

func setupCronHandlerRuntimeTest(t *testing.T) {
	t.Helper()
	setupBackupOverviewTestDB(t)
	insertBackupPolicySite(t, 1, "cron.example.com")
	if _, err := database.GetDB().Exec(`INSERT INTO cron_jobs
		(id,name,cron_expression,command,task_type,site_id,enabled)
		VALUES(41,'site cron','* * * * *','cron.example.com','wp_cron',1,1)`); err != nil {
		t.Fatal(err)
	}
}

func TestCronListReportsPausedAndMigrationRuntimeStates(t *testing.T) {
	setupCronHandlerRuntimeTest(t)
	db := database.GetDB()
	if _, err := db.Exec(`UPDATE websites SET status='paused' WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	readState := func() map[string]interface{} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		new(CronHandler).List(ctx)
		if recorder.Code != http.StatusOK {
			t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Data []map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || len(response.Data) != 1 {
			t.Fatalf("list response=%s err=%v", recorder.Body.String(), err)
		}
		return response.Data[0]
	}

	paused := readState()
	if paused["runtime_reason"] != "paused" || paused["runtime_suspended"] != true {
		t.Fatalf("paused runtime state=%v", paused)
	}

	if _, err := db.Exec(`UPDATE websites SET status='active' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	// The gate only reads site_id/status. Avoid building an unrelated migration task fixture.
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_migration_locks
		(domain,site_id,migration_site_id,direction,status)
		VALUES('cron.example.com',1,'handler_migration_test','source','active')`); err != nil {
		t.Fatal(err)
	}
	migrating := readState()
	if migrating["runtime_reason"] != "migration_locked" || migrating["runtime_suspended"] != true {
		t.Fatalf("migration runtime state=%v", migrating)
	}
}

func TestCronRunCannotConfirmPastMigrationLock(t *testing.T) {
	setupCronHandlerRuntimeTest(t)
	db := database.GetDB()
	// The handler only reads the active lock. Its parent migration row is outside this test.
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_migration_locks
		(domain,site_id,migration_site_id,direction,status)
		VALUES('cron.example.com',1,'handler_confirm_test','source','active')`); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/cron/41/run?confirm_paused=1", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "41"}}
	new(CronHandler).Run(ctx)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("migration run status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
