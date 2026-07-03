package kubernetes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/meshery/meshery/server/machines"
	"github.com/meshery/meshery/server/models"
	"github.com/meshery/meshkit/logger"
	meshkitkube "github.com/meshery/meshkit/utils/kubernetes"
	"github.com/meshery/schemas/models/core"
	clientgo "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// TestAssignInitialCtx_AttachesLoggerBeforeClientSetAssignment guards against
// a nil-pointer panic on login when a persisted K8s context can't be reached:
// GenerateKubeHandler errors → GenerateK8sClientSet hits its log.Warn path →
// interface-method-on-nil panic.
//
// The bug was an ordering mistake in AssignInitialCtx: machinectx.log was
// assigned AFTER AssignClientSetToContext, which threaded the still-nil
// log through GenerateClientSetAction → GenerateK8sClientSet. Any persisted
// context whose API server wasn't reachable (common: stale contexts from a
// remote provider pointing at clusters this host can't route to) produced
// the panic on every /api request that went through K8sFSMMiddleware.
//
// This test exercises AssignInitialCtx with a K8sContext whose API server
// is unreachable, which forces the error path previously responsible for
// the nil-deref panic. The assertions:
//   - no panic on the error path
//   - AssignInitialCtx surfaces the underlying AssignClientSetToContext error
//     (so the caller can handle it) and does not return a populated context
//   - machinectx.log is the exact logger we passed (proving the attach
//     happened before any action could consume it)
func TestAssignInitialCtx_AttachesLoggerBeforeClientSetAssignment(t *testing.T) {
	log, err := logger.New("test", logger.Options{})
	if err != nil {
		t.Fatalf("failed to build test logger: %v", err)
	}

	// Fail fast on UUID generation so the event builder always sees a valid
	// user ID and the test setup is deterministic; silently leaving user.ID
	// unset would change the code path we're exercising.
	userID, err := uuid.NewV4()
	if err != nil {
		t.Fatalf("failed to generate user UUID: %v", err)
	}
	user := &models.User{ID: core.Uuid(userID)}

	sysID := core.Uuid(uuid.FromStringOrNil("00000000-0000-0000-0000-000000000000"))

	ctx := context.Background()
	ctx = context.WithValue(ctx, models.UserCtxKey, user)
	ctx = context.WithValue(ctx, models.SystemIDKey, &sysID)
	// ProviderCtxKey: a typed-nil is fine — AssignControllerHandlers is only
	// reached after AssignClientSetToContext, and that's the point we want
	// to defend. If AssignClientSetToContext returns an error we never reach
	// controller setup, which matches the production scenario.
	var provider models.Provider
	ctx = context.WithValue(ctx, models.ProviderCtxKey, provider)

	machinectx := &MachineCtx{
		K8sContext: models.K8sContext{
			// Deliberately empty: any unreachable/invalid kubeconfig is fine.
			// The point is to force GenerateKubeHandler to fail so the
			// previously panicking log.Warn path runs.
			Name:         "unreachable-test-context",
			Server:       "https://127.0.0.1:1", // RFC-reserved, refused instantly
			ConnectionID: uuid.Must(uuid.NewV4()).String(),
		},
		// clientset left nil to force AssignClientSetToContext to attempt
		// GenerateClientSetAction (the panicking path).
	}

	result, _, err := AssignInitialCtx(ctx, machinectx, log)

	// AssignClientSetToContext must fail for an unreachable/invalid context —
	// that's the exact production regression we're guarding. If this ever
	// returns nil here, either the test lost its repro or GenerateK8sClientSet
	// started tolerating unreachable servers, both of which invalidate this
	// guard.
	if err == nil {
		t.Fatal("expected AssignInitialCtx to return an error for an unreachable K8s context, got nil — the regression guard is no longer exercising the panicking path")
	}
	if result != nil {
		t.Fatalf("expected nil machine context on AssignClientSetToContext error, got %#v", result)
	}

	// Logger must be the exact instance we passed in: equality (not just
	// non-nil) proves the attach happened before AssignClientSetToContext
	// ran, which is the invariant the ordering fix establishes.
	if machinectx.log != log {
		t.Fatal("expected machinectx.log to be the logger passed into AssignInitialCtx and assigned before AssignClientSetToContext; a different or nil value reintroduces the login-panic ordering bug")
	}
}

// TestDiscoverAction_ReturnsDisconnectWhenServerIDCannotBeAssigned guards
// against the connection remaining in NOTFOUND when Meshery loses
// authorization to read the kube-system namespace.
//
// Previously, an AssignServerID failure emitted machines.NotFound, leaving
// the connection eligible for future discovery attempts. The fix changes
// this transition to machines.Disconnect so the FSM persists the connection
// as DISCONNECTED and stops repeatedly re-managing the cluster.
//
// The test simulates the Kubernetes API returning HTTP 403 for the
// kube-system namespace lookup and verifies DiscoverAction emits the
// Disconnect event without assigning a KubernetesServerID.
func TestDiscoverAction_ReturnsDisconnectWhenServerIDCannotBeAssigned(t *testing.T) {
	log, err := logger.New("test", logger.Options{})
	if err != nil {
		t.Fatalf("failed to build test logger: %v", err)
	}

	userID, err := uuid.NewV4()
	if err != nil {
		t.Fatalf("failed to generate user UUID: %v", err)
	}
	user := &models.User{ID: core.Uuid(userID)}

	sysID := core.Uuid(uuid.Nil)
	ctx := context.Background()
	ctx = context.WithValue(ctx, models.UserCtxKey, user)
	ctx = context.WithValue(ctx, models.SystemIDKey, &sysID)
	ctx = context.WithValue(ctx, models.ProviderCtxKey, models.Provider(nil))
	ctx = context.WithValue(ctx, models.TokenCtxKey, "test-token")

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/namespaces/kube-system" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"kind":"Status","message":"forbidden"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	restConfig := &rest.Config{
		Host: server.URL,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
	}
	clientset, err := clientgo.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("failed to build clientset: %v", err)
	}

	machinectx := &MachineCtx{
		K8sContext: models.K8sContext{
			Name:         "forbidden-context",
			Server:       server.URL,
			ConnectionID: uuid.Must(uuid.NewV4()).String(),
		},
		clientset: &meshkitkube.Client{KubeClient: clientset},
		log:       log,
	}

	action := &DiscoverAction{}
	eventType, event, err := action.Execute(ctx, machinectx, nil)
	if err == nil {
		t.Fatal("expected DiscoverAction.Execute to fail when kube-system is forbidden, got nil error")
	}
	if eventType != machines.Disconnect {
		t.Fatalf("expected event type %q, got %q", machines.Disconnect, eventType)
	}
	if event == nil {
		t.Fatal("expected DiscoverAction to emit an error event on AssignServerID failure")
	}

	if machinectx.K8sContext.KubernetesServerID != nil {
		t.Fatal("expected KubernetesServerID to remain unset after AssignServerID failure")
	}
}
