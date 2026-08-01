package test

import (
	"encoding/json"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/types"
	"expo-open-ota/internal/update"
	"fmt"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func createRollbackRequest(projectRoot, branch, runtimeVersion, headerKey, headerValue, platform, commitHash string) (*httptest.ResponseRecorder, *mux.Router, *mux.Route, *http.Request) {
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	var q string
	if commitHash != "" {
		q = fmt.Sprintf("http://localhost:3000/rollback/%s?runtimeVersion=%s&platform=%s&commitHash=%s", branch, runtimeVersion, platform, commitHash)
	} else {
		q = fmt.Sprintf("http://localhost:3000/rollback/%s?runtimeVersion=%s&platform=%s", branch, runtimeVersion, platform)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r = mux.SetURLVars(r, map[string]string{"BRANCH": branch})
	r.Header.Set(headerKey, headerValue)
	return w, mux.NewRouter(), nil, r
}

func TestToRollbackWithBadBearer(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	w, _, _, r := createRollbackRequest(projectRoot, "DO_NOT_USE", "1", "Authorization", "Bearer expo_bad_token", "ios", "hash")
	handlers.RollbackHandler(w, r)
	assert.Equal(t, 401, w.Code, "Expected status code 401")
	assert.Equal(t, "Error fetching expo account informations\n", w.Body.String(), "Expected error message")
}

func TestGoodRollback(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	w, _, _, r := createRollbackRequest(projectRoot, "DO_NOT_USE", "1", "Authorization", "Bearer expo_test_token", "ios", "hash")
	handlers.RollbackHandler(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200")
	type Response struct {
		Branch         string `json:"branch"`
		RuntimeVersion string `json:"runtimeVersion"`
		UpdateId       string `json:"updateId"`
		CreatedAt      int64  `json:"createdAt"`
	}

	var body Response
	err = json.Unmarshal(w.Body.Bytes(), &body)
	require.NoError(t, err)

	assert.NotEmpty(t, body.UpdateId, "Expected non-empty updateId")
	assert.NotEmpty(t, body.RuntimeVersion, "Expected non-empty runtimeVersion")
	assert.NotEmpty(t, body.Branch, "Expected non-empty branch")
	assert.NotEmpty(t, body.CreatedAt, "Expected non-empty createdAt")
	lastUpdate, err := update.GetLatestUpdateBundlePathForRuntimeVersion("DO_NOT_USE", "1", "ios")
	if err != nil {
		t.Fatalf("Error getting latest update: %v", err)
	}
	assert.Equal(t, body.UpdateId, lastUpdate.UpdateId, "Expected updateId to match the latest update")
	updateType := update.GetUpdateType(*lastUpdate)
	assert.Equal(t, updateType, types.Rollback, "Expected update type to be rollback")
}

func TestGoodRollbackWithoutCommitHash(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	w, _, _, r := createRollbackRequest(projectRoot, "DO_NOT_USE", "1", "Authorization", "Bearer expo_test_token", "ios", "")
	handlers.RollbackHandler(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200")
	type Response struct {
		Branch         string `json:"branch"`
		RuntimeVersion string `json:"runtimeVersion"`
		UpdateId       string `json:"updateId"`
		CreatedAt      int64  `json:"createdAt"`
	}

	var body Response
	err = json.Unmarshal(w.Body.Bytes(), &body)
	require.NoError(t, err)

	assert.NotEmpty(t, body.UpdateId, "Expected non-empty updateId")
	assert.NotEmpty(t, body.RuntimeVersion, "Expected non-empty runtimeVersion")
	assert.NotEmpty(t, body.Branch, "Expected non-empty branch")
	assert.NotEmpty(t, body.CreatedAt, "Expected non-empty createdAt")
	lastUpdate, err := update.GetLatestUpdateBundlePathForRuntimeVersion("DO_NOT_USE", "1", "ios")
	if err != nil {
		t.Fatalf("Error getting latest update: %v", err)
	}
	assert.Equal(t, body.UpdateId, lastUpdate.UpdateId, "Expected updateId to match the latest update")
	updateType := update.GetUpdateType(*lastUpdate)
	assert.Equal(t, updateType, types.Rollback, "Expected update type to be rollback")
}

// Guards the ordering in MarkUpdateAsChecked: the .check file must land before
// the cache invalidation. StoreUpdateUUIDInMetadata sits between them doing slow
// bucket I/O, and a /manifest request inside that window sees the new update
// without its .check file, filters it out via IsUpdateValid, falls back to the
// previous update and re-caches that one for the full TTL.
func TestRollbackDoesNotPoisonLatestUpdateCache(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	const branchName = "DO_NOT_USE" // GlobalAfterEach cleans this branch under ./updates
	const runtimeVersion = "1"
	const platform = "android"
	const staleUpdateId = "1700000000000"

	// Same LOCAL_BUCKET_BASE_PATH as createRollbackRequest, so the bucket
	// singleton built here is the one the rollback handler writes to.
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))

	// A previous update for the cache to latch onto; without it there is nothing
	// stale to serve and the race stays invisible.
	stalePath := filepath.Join(projectRoot, "./updates", branchName, runtimeVersion, staleUpdateId)
	require.NoError(t, os.MkdirAll(stalePath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stalePath, "update-metadata.json"),
		[]byte(`{"platform":"android","commitHash":"stale","updateUUID":"00000000-0000-0000-0000-000000000001"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stalePath, ".check"), []byte(".check"), 0o644))

	warmed, err := update.GetLatestUpdateBundlePathForRuntimeVersion(branchName, runtimeVersion, platform)
	require.NoError(t, err)
	require.NotNil(t, warmed, "fixture setup: planted update not picked up, check LOCAL_BUCKET_BASE_PATH wiring")
	require.Equal(t, staleUpdateId, warmed.UpdateId, "fixture setup: expected cache warmed with planted update")

	// Read past the handler's return, so a poisoned value must survive to the assert.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var reads int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = update.GetLatestUpdateBundlePathForRuntimeVersion(branchName, runtimeVersion, platform)
					atomic.AddInt64(&reads, 1)
				}
			}
		}()
	}

	wRollback, _, _, rRollback := createRollbackRequest(projectRoot, branchName, runtimeVersion, "Authorization", "Bearer expo_test_token", platform, "hash")
	handlers.RollbackHandler(wRollback, rRollback)
	require.Equal(t, http.StatusOK, wRollback.Code, "rollback handler failed: %s", wRollback.Body.String())

	close(stop)
	wg.Wait()

	latest, err := update.GetLatestUpdateBundlePathForRuntimeVersion(branchName, runtimeVersion, platform)
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.NotEqual(t, staleUpdateId, latest.UpdateId, "lastUpdate cache poisoned with stale update after %d reads", reads)
	assert.Equal(t, types.Rollback, update.GetUpdateType(*latest), "expected latest update to be a rollback")
}
