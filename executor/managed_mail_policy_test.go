package executor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func setupManagedMailPolicyTest(t *testing.T) (*ManagedMailPolicyService, int64) {
	t.Helper()
	previous := database.DB
	if err := database.Open(filepath.Join(t.TempDir(), "managed-mail.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	t.Cleanup(func() {
		_ = db.Close()
		database.DB = previous
	})
	if err := database.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := database.RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO websites
		(name,domain,status,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path)
		VALUES ('Managed Mail','mail.example.test','active','wordpress','wp_mail','/srv/mail','/var/log/mail','wp_mail','wp_mail','/etc/php/mail.conf','/etc/nginx/mail.conf')`)
	if err != nil {
		t.Fatal(err)
	}
	siteID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{
		"smtp_host":       "smtp.example.test",
		"smtp_port":       "587",
		"smtp_encryption": "starttls",
		"smtp_user":       "api-user",
		"smtp_pass":       "secret",
		"smtp_from_email": "noreply@example.test",
	}
	for key, value := range settings {
		if _, err := db.Exec(`INSERT INTO security_settings(skey,svalue) VALUES(?,?) ON CONFLICT(skey) DO UPDATE SET svalue=excluded.svalue`, key, value); err != nil {
			t.Fatal(err)
		}
	}
	service := NewManagedMailPolicyService(db)
	service.now = func() time.Time { return time.Date(2026, 10, 12, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60)) }
	return service, siteID
}

func authorizeManagedMailSite(t *testing.T, service *ManagedMailPolicyService, siteID int64, minute, hour, day int) {
	t.Helper()
	ctx := context.Background()
	policy, err := service.SaveSitePolicy(ctx, ManagedMailSitePolicy{
		SiteID: siteID, Authorized: true, FromName: "Example Site", ReplyTo: "support@example.test",
		LockFromName: true, MinuteLimit: minute, HourLimit: hour, DayLimit: day,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Authorized || policy.SiteConfirmed || !policy.SMTPReady || policy.Active {
		t.Fatalf("unexpected policy before confirmation: %+v", policy)
	}
	policy, err = service.ConfirmSite(ctx, siteID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Active {
		t.Fatalf("policy should be active after double opt-in: %+v", policy)
	}
}

func TestManagedMailPolicyRequiresDoubleOptInAndDoesNotAutoResumeConflict(t *testing.T) {
	service, siteID := setupManagedMailPolicyTest(t)
	ctx := context.Background()
	authorizeManagedMailSite(t, service, siteID, 3, 15, 50)

	policy, err := service.ReportConflict(ctx, siteID, "post-smtp/postman-smtp.php")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Active || policy.SiteConfirmed || policy.ConflictPlugin == "" {
		t.Fatalf("conflict did not pause and clear confirmation: %+v", policy)
	}
	policy, err = service.ReportConflict(ctx, siteID, "")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Active || policy.SiteConfirmed || policy.ConflictPlugin != "" {
		t.Fatalf("cleared conflict must still require explicit confirmation: %+v", policy)
	}

	policy, err = service.ConfirmSite(ctx, siteID, true)
	if err != nil || !policy.Active {
		t.Fatalf("explicit re-confirmation failed: policy=%+v err=%v", policy, err)
	}
	policy, err = service.SaveSitePolicy(ctx, ManagedMailSitePolicy{
		SiteID: siteID, Authorized: false, FromName: policy.FromName,
		ReplyTo: policy.ReplyTo, LockFromName: policy.LockFromName,
		MinuteLimit: policy.MinuteLimit, HourLimit: policy.HourLimit, DayLimit: policy.DayLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Authorized || policy.SiteConfirmed || policy.Active {
		t.Fatalf("panel revocation must clear site confirmation: %+v", policy)
	}
}

func TestManagedMailConfirmDistinguishesSMTPNotReady(t *testing.T) {
	service, siteID := setupManagedMailPolicyTest(t)
	ctx := context.Background()
	if _, err := service.SaveSitePolicy(ctx, ManagedMailSitePolicy{
		SiteID: siteID, Authorized: true, MinuteLimit: 3, HourLimit: 15, DayLimit: 50,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`UPDATE security_settings SET svalue='' WHERE skey='smtp_from_email'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmSite(ctx, siteID, true); !errors.Is(err, ErrManagedMailUnavailable) {
		t.Fatalf("confirm without ready SMTP error=%v, want ErrManagedMailUnavailable", err)
	}
}

func TestManagedMailDefaultsEmptyFromEmailToSMTPUser(t *testing.T) {
	service, siteID := setupManagedMailPolicyTest(t)
	ctx := context.Background()
	if _, err := service.SaveSitePolicy(ctx, ManagedMailSitePolicy{
		SiteID: siteID, Authorized: true, MinuteLimit: 3, HourLimit: 15, DayLimit: 50,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`UPDATE security_settings SET svalue='' WHERE skey='smtp_from_email'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`UPDATE security_settings SET svalue='sender@example.test' WHERE skey='smtp_user'`); err != nil {
		t.Fatal(err)
	}
	policy, err := service.GetSitePolicy(ctx, siteID)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.SMTPReady || policy.FromEmail != "sender@example.test" {
		t.Fatalf("empty From Email did not default to SMTP username: %+v", policy)
	}
	if _, err := service.ConfirmSite(ctx, siteID, true); err != nil {
		t.Fatalf("confirmation should accept the valid SMTP username default: %v", err)
	}
}

func TestManagedMailPrefersExplicitFromEmail(t *testing.T) {
	service, siteID := setupManagedMailPolicyTest(t)
	policy, err := service.GetSitePolicy(context.Background(), siteID)
	if err != nil {
		t.Fatal(err)
	}
	if policy.FromEmail != "noreply@example.test" {
		t.Fatalf("explicit From Email was not preserved: site=%d policy=%+v", siteID, policy)
	}
}

func TestManagedMailSiteSenderUpdateHonorsAuthorizationAndFromNameLock(t *testing.T) {
	service, siteID := setupManagedMailPolicyTest(t)
	ctx := context.Background()
	if _, err := service.UpdateSiteSender(ctx, siteID, "Site Name", "owner@example.test"); !errors.Is(err, ErrManagedMailUnauthorized) {
		t.Fatalf("unauthorized sender update error=%v", err)
	}
	policy, err := service.SaveSitePolicy(ctx, ManagedMailSitePolicy{
		SiteID: siteID, Authorized: true, FromName: "Panel Name", ReplyTo: "panel@example.test",
		LockFromName: true, MinuteLimit: 3, HourLimit: 15, DayLimit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateSiteSender(ctx, siteID, "Bypass", "owner@example.test"); !errors.Is(err, ErrManagedMailFromNameLock) {
		t.Fatalf("locked sender update error=%v", err)
	}
	policy, err = service.UpdateSiteSender(ctx, siteID, policy.FromName, "owner@example.test")
	if err != nil || policy.FromName != "Panel Name" || policy.ReplyTo != "owner@example.test" {
		t.Fatalf("reply-to-only update policy=%+v err=%v", policy, err)
	}
}

func TestManagedMailReservationIsIdempotentAndFailedSendReleasesQuota(t *testing.T) {
	service, siteID := setupManagedMailPolicyTest(t)
	authorizeManagedMailSite(t, service, siteID, 3, 15, 50)
	ctx := context.Background()

	first, err := service.Reserve(ctx, siteID, "request-idempotent-0001", 2, 1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.Reserve(ctx, siteID, "request-idempotent-0001", 2, 1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Existing || first.RequestID != again.RequestID || again.Status != "reserved" {
		t.Fatalf("unexpected idempotent reservation: first=%+v again=%+v", first, again)
	}
	if err := service.Complete(ctx, siteID, first.RequestID, "rejected", "", "smtp_rejected", 550, 20); err != nil {
		t.Fatal(err)
	}
	second, err := service.Reserve(ctx, siteID, "request-after-reject-0002", 3, 2048, 0)
	if err != nil {
		t.Fatalf("rejected delivery should release quota: %v", err)
	}
	if second.Usage.SiteMinute != 3 {
		t.Fatalf("site minute usage=%d, want 3", second.Usage.SiteMinute)
	}
	if err := service.Complete(ctx, siteID, second.RequestID, "unknown", "", "data_ambiguous", 0, 50); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reserve(ctx, siteID, "request-over-quota-0003", 1, 10, 0); !errors.Is(err, ErrManagedMailQuota) {
		t.Fatalf("unknown delivery must keep quota consumed, got %v", err)
	}
}

func TestManagedMailConcurrentReservationsCannotOvershootQuota(t *testing.T) {
	service, siteID := setupManagedMailPolicyTest(t)
	authorizeManagedMailSite(t, service, siteID, 3, 3, 3)
	ctx := context.Background()

	const attempts = 8
	start := make(chan struct{})
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := service.Reserve(ctx, siteID, fmt.Sprintf("request-concurrent-%02d", i), 1, 100, 0)
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	succeeded, quotaRejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrManagedMailQuota):
			quotaRejected++
		default:
			t.Fatalf("unexpected concurrent reservation error: %v", err)
		}
	}
	if succeeded != 3 || quotaRejected != attempts-3 {
		t.Fatalf("succeeded=%d quotaRejected=%d", succeeded, quotaRejected)
	}
	var recipients int
	if err := service.db.QueryRow(`SELECT COALESCE(SUM(recipient_count),0) FROM managed_mail_deliveries WHERE status='reserved'`).Scan(&recipients); err != nil {
		t.Fatal(err)
	}
	if recipients != 3 {
		t.Fatalf("reserved recipients=%d, want 3", recipients)
	}
}

func TestManagedMailDeliveryPolicyPausesGlobalAuthAndConsecutiveSiteFailures(t *testing.T) {
	service, siteID := setupManagedMailPolicyTest(t)
	authorizeManagedMailSite(t, service, siteID, 20, 20, 20)
	ctx := context.Background()
	if err := service.ApplyDeliveryPolicy(ctx, siteID, "rejected", "authentication_failed"); err != nil {
		t.Fatal(err)
	}
	policy, err := service.GetSitePolicy(ctx, siteID)
	if err != nil || policy.Active || policy.PausedReason != "authentication_failed" || policy.PausedUntil == nil {
		t.Fatalf("authentication pause policy=%+v err=%v", policy, err)
	}
	if _, err := service.db.Exec(`UPDATE managed_mail_sites SET paused_until=NULL,paused_reason='' WHERE site_id=?`, siteID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := service.db.Exec(`INSERT INTO managed_mail_deliveries(request_id,site_id,status,recipient_count,message_size,created_at,updated_at) VALUES(?,?, 'temporary_failure',1,10,?,?)`, fmt.Sprintf("failure-sequence-%02d", i), siteID, service.now().UTC(), service.now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.ApplyDeliveryPolicy(ctx, siteID, "temporary_failure", "smtp_temporary_failure"); err != nil {
		t.Fatal(err)
	}
	policy, err = service.GetSitePolicy(ctx, siteID)
	if err != nil || policy.Active || policy.PausedReason != "consecutive_failures" {
		t.Fatalf("consecutive failure policy=%+v err=%v", policy, err)
	}
}

func TestManagedMailQuotaNoticesAreThresholdedAndDeduplicated(t *testing.T) {
	service, siteID := setupManagedMailPolicyTest(t)
	authorizeManagedMailSite(t, service, siteID, 10, 10, 10)
	ctx := context.Background()
	emails := make(chan struct{}, 2)
	previous := managedMailQuotaEmail
	managedMailQuotaEmail = func(string, string, string) error { emails <- struct{}{}; return nil }
	t.Cleanup(func() { managedMailQuotaEmail = previous })
	insertAccepted := func(start, count int) {
		for i := start; i < start+count; i++ {
			if _, err := service.db.Exec(`INSERT INTO managed_mail_deliveries(request_id,site_id,status,recipient_count,message_size,created_at,updated_at) VALUES(?,?, 'accepted',1,10,?,?)`, fmt.Sprintf("quota-notice-%04d", i), siteID, service.now().UTC(), service.now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
	}
	insertAccepted(0, 7)
	if err := service.ProcessQuotaNotices(ctx, siteID); err != nil {
		t.Fatal(err)
	}
	insertAccepted(7, 2)
	if err := service.ProcessQuotaNotices(ctx, siteID); err != nil {
		t.Fatal(err)
	}
	insertAccepted(9, 1)
	if err := service.ProcessQuotaNotices(ctx, siteID); err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessQuotaNotices(ctx, siteID); err != nil {
		t.Fatal(err)
	}
	var notices int
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type='alert_managed_mail_quota'`).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	select {
	case <-emails:
	case <-time.After(time.Second):
		t.Fatal("quota email was not scheduled")
	}
	select {
	case <-emails:
		t.Fatal("quota email was not deduplicated")
	case <-time.After(20 * time.Millisecond):
	}
	if notices != 3 {
		t.Fatalf("notices=%d", notices)
	}
}

func TestSystemMailDailyProtectionLimit(t *testing.T) {
	service, _ := setupManagedMailPolicyTest(t)
	if _, err := service.db.Exec(`UPDATE security_settings SET svalue='2' WHERE skey='managed_mail_system_day_limit'`); err != nil {
		t.Fatal(err)
	}
	now := service.now()
	if err := reserveSystemMailAttempt(service.db, now); err != nil {
		t.Fatal(err)
	}
	if err := reserveSystemMailAttempt(service.db, now); err != nil {
		t.Fatal(err)
	}
	if err := reserveSystemMailAttempt(service.db, now); err == nil {
		t.Fatal("system mail protection accepted an attempt over the daily limit")
	}
}
