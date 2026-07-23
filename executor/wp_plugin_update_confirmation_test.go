package executor

import (
	"sync"
	"testing"
)

func TestWPPluginConfirmationBindsComponentAndConsumesOnce(t *testing.T) {
	store := newWPPluginConfirmationStore()
	record, err := store.create(wpPluginConfirmation{
		username: "admin", siteID: 1, domain: "example.com", collectionID: "collection-a",
		componentKey: "sample/sample.php", currentVersion: "1.0.0", targetVersion: "1.1.0",
		downloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.consume(record.token, "admin", 1, "other/other.php", "1.1.0"); err == nil {
		t.Fatal("token accepted for another component")
	}
	const consumers = 8
	var wg sync.WaitGroup
	results := make(chan error, consumers)
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.consume(record.token, "admin", 1, "sample/sample.php", "1.1.0")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful token consumptions=%d", success)
	}
}
