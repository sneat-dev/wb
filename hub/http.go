package hub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/hub/narrate"
)

const (
	WebhookPath = APIPrefix + "/github/webhook"
	// MinimumWebhookSecretBytes is the configuration-readiness floor for a
	// high-entropy GitHub webhook secret.
	MinimumWebhookSecretBytes      = 32
	maxJSONBodyBytes               = 8 << 20
	maxWebhookBodyBytes            = 25 << 20
	installationContinuationCookie = "__Secure-wb_github_installation"
	openerChallengeCookie          = "__Secure-wb_github_opener"
	oauthCredentialCookie          = "__Secure-wb_github_oauth"
	installationContinuationPath   = APIPrefix + "/github/installations/"
	installationOpenerOrigin       = "https://sneat.work"
)

type HandlerOptions struct {
	ViewerResolver   ViewerResolver
	MachineBearer    MachineBearerResolver
	Enrollment       *MachineEnrollmentService
	Snapshots        *MachineSnapshotService
	Installations    *InstallationConnectionService
	RepositoryEvents *RepositoryEventService
	Status           *StatusService
	Projection       ProjectionProcessor
	WebhookSecret    []byte
	AllowedOrigin    string
	// Narrate receives one line for every delivery the handler itself
	// rejects, written before the response to GitHub is sent. Deliveries the
	// handler accepts are narrated by RepositoryEventService instead, so each
	// delivery produces exactly one line. Optional.
	Narrate func(narrate.Line)
}

func NewHandler(options HandlerOptions) http.Handler {
	if len(options.WebhookSecret) < MinimumWebhookSecretBytes {
		options.WebhookSecret = nil
	} else {
		options.WebhookSecret = append([]byte(nil), options.WebhookSecret...)
	}
	handler := apiHandler{options: options}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+MachineEnrollmentPath, handler.enroll)
	mux.HandleFunc("POST "+machinesnapshot.SnapshotPath, handler.publishSnapshot)
	mux.HandleFunc("GET "+machinesnapshot.SnapshotPath, handler.listSnapshots)
	mux.HandleFunc("POST "+InstallationConnectPath, handler.connectInstallation)
	mux.HandleFunc("GET "+InstallationContinuePath, handler.continueInstallation)
	mux.HandleFunc("POST "+InstallationAuthorizePath, handler.authorizeInstallationOpener)
	mux.HandleFunc("GET "+InstallationSetupPath, handler.completeSetup)
	mux.HandleFunc("GET "+InstallationCallbackPath, handler.completeOAuth)
	mux.HandleFunc("GET "+repositoryevent.EventsPath, handler.pollEvents)
	mux.HandleFunc("POST "+repositoryevent.AckPath, handler.ackEvents)
	mux.HandleFunc("POST "+WebhookPath, handler.webhook)
	mux.HandleFunc("GET "+StatusPath, handler.status)
	return cors(options.AllowedOrigin, mux)
}

func (h apiHandler) status(w http.ResponseWriter, r *http.Request) {
	viewer, ok := h.viewer(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "viewer_unauthorized")
		return
	}
	if h.options.Status == nil {
		writeError(w, http.StatusServiceUnavailable, "github_status_unavailable")
		return
	}
	response, err := h.options.Status.Read(r.Context(), viewer)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "github_status_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

type apiHandler struct{ options HandlerOptions }

func (h apiHandler) viewer(r *http.Request) (Viewer, bool) {
	if h.options.ViewerResolver == nil {
		return Viewer{}, false
	}
	v, e := h.options.ViewerResolver.Viewer(r)
	return v, e == nil && v.Authenticated && strings.TrimSpace(v.IdentityID) != ""
}

func (h apiHandler) machine(r *http.Request) (Machine, bool) {
	if h.options.MachineBearer == nil {
		return Machine{}, false
	}
	m, e := h.options.MachineBearer.ResolveMachineBearer(r)
	return m, e == nil && m.valid()
}

func (h apiHandler) enroll(w http.ResponseWriter, r *http.Request) {
	v, ok := h.viewer(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "viewer_unauthorized")
		return
	}
	if h.options.Enrollment == nil {
		writeError(w, http.StatusServiceUnavailable, "machine_enrollment_unavailable")
		return
	}
	var body MachineEnrollmentRequest
	if decodeRequest(w, r, 16<<10, &body) != nil {
		writeError(w, http.StatusBadRequest, "invalid_machine_enrollment")
		return
	}
	response, err := h.options.Enrollment.Enroll(r.Context(), v, body)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "machine_enrollment_unavailable")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_machine_enrollment")
		}
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h apiHandler) publishSnapshot(w http.ResponseWriter, r *http.Request) {
	m, ok := h.machine(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "machine_bearer_unavailable")
		return
	}
	if h.options.Snapshots == nil {
		writeError(w, http.StatusServiceUnavailable, "machine_snapshot_unavailable")
		return
	}
	var body machinesnapshot.Snapshot
	if decodeRequest(w, r, maxJSONBodyBytes, &body) != nil {
		writeError(w, http.StatusBadRequest, "invalid_machine_snapshot")
		return
	}
	response, err := h.options.Snapshots.Publish(r.Context(), m, body)
	if err != nil {
		status := http.StatusServiceUnavailable
		if !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrUnauthorized) {
			status = http.StatusBadRequest
		}
		writeError(w, status, "machine_snapshot_failed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h apiHandler) listSnapshots(w http.ResponseWriter, r *http.Request) {
	m, ok := h.machine(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "machine_bearer_unavailable")
		return
	}
	if h.options.Snapshots == nil {
		writeError(w, http.StatusServiceUnavailable, "machine_snapshot_unavailable")
		return
	}
	response, err := h.options.Snapshots.List(r.Context(), m)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "machine_snapshot_failed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h apiHandler) connectInstallation(w http.ResponseWriter, r *http.Request) {
	v, ok := h.viewer(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "viewer_unauthorized")
		return
	}
	if h.options.Installations == nil {
		writeError(w, http.StatusServiceUnavailable, "installation_connection_unavailable")
		return
	}
	var body InstallationConnectRequest
	if decodeRequest(w, r, 16<<10, &body) != nil {
		writeError(w, http.StatusBadRequest, "invalid_installation_connection")
		return
	}
	response, err := h.options.Installations.Begin(r.Context(), v, body)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			writeError(w, http.StatusForbidden, "installation_connection_forbidden")
		} else {
			writeError(w, http.StatusServiceUnavailable, "installation_connection_unavailable")
		}
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h apiHandler) continueInstallation(w http.ResponseWriter, r *http.Request) {
	if h.options.Installations == nil {
		writeError(w, http.StatusServiceUnavailable, "installation_connection_unavailable")
		return
	}
	state := r.URL.Query().Get("state")
	result, err := h.options.Installations.Continue(r.Context(), state, installationCookie(r, openerChallengeCookie))
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "installation_connection_unavailable")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_installation_continuation")
		}
		return
	}
	if result.RedirectURL == "" {
		if result.openerChallenge != "" {
			setInstallationCookie(w, openerChallengeCookie, result.openerChallenge, result.ExpiresAt)
		}
		writeInstallationOpenerPage(w, state, result.openerChallenge, result.inert)
		return
	}
	setInstallationContinuationCookie(w, result.browserContinuation, result.ExpiresAt)
	clearInstallationCookie(w, openerChallengeCookie)
	http.Redirect(w, r, result.RedirectURL, http.StatusSeeOther)
}

func (h apiHandler) authorizeInstallationOpener(w http.ResponseWriter, r *http.Request) {
	viewer, ok := h.viewer(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "viewer_unauthorized")
		return
	}
	if h.options.Installations == nil {
		writeError(w, http.StatusServiceUnavailable, "installation_connection_unavailable")
		return
	}
	if r.Header.Get("Origin") != installationOpenerOrigin {
		writeError(w, http.StatusForbidden, "installation_authorization_forbidden")
		return
	}
	var request InstallationOpenerAuthorizationRequest
	if decodeRequest(w, r, 16<<10, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_installation_authorization")
		return
	}
	if err := h.options.Installations.AuthorizeOpener(r.Context(), viewer, request); err != nil {
		switch {
		case errors.Is(err, ErrUnauthorized):
			writeError(w, http.StatusForbidden, "installation_authorization_forbidden")
		case errors.Is(err, ErrUnavailable):
			writeError(w, http.StatusServiceUnavailable, "installation_connection_unavailable")
		default:
			writeError(w, http.StatusBadRequest, "invalid_installation_authorization")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeInstallationOpenerPage(w http.ResponseWriter, state, challenge string, inert bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	stateJSON, _ := json.Marshal(state)
	challengeJSON, _ := json.Marshal(challenge)
	if inert {
		_, _ = io.WriteString(w, "<!doctype html><title>GitHub connection unavailable</title><p>Return to Sneat Work and start the GitHub connection again.</p>")
		return
	}
	_, _ = io.WriteString(w, `<!doctype html><title>Connecting GitHub</title><p>Continue in the Sneat Work window.</p><script>(()=>{const state=`+string(stateJSON)+`;const challenge=`+string(challengeJSON)+`;const opener=window.opener;if(!opener)return;opener.postMessage({type:"workbench-github-installation-challenge",state,challenge},"`+installationOpenerOrigin+`");window.addEventListener("message",event=>{if(event.origin!=="`+installationOpenerOrigin+`"||event.source!==opener)return;const data=event.data;if(!data||data.type!=="workbench-github-installation-authorized"||data.challenge!==challenge)return;window.location.reload();});})();</script>`)
}

func (h apiHandler) completeSetup(w http.ResponseWriter, r *http.Request) {
	if h.options.Installations == nil {
		writeError(w, http.StatusServiceUnavailable, "installation_connection_unavailable")
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("installation_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_installation_setup")
		return
	}
	result, err := h.options.Installations.CompleteSetup(r.Context(), r.URL.Query().Get("state"), id, installationContinuation(r))
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "installation_connection_unavailable")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_installation_setup")
		}
		return
	}
	http.Redirect(w, r, result.RedirectURL, http.StatusSeeOther)
}

func (h apiHandler) completeOAuth(w http.ResponseWriter, r *http.Request) {
	if h.options.Installations == nil {
		writeError(w, http.StatusServiceUnavailable, "installation_connection_unavailable")
		return
	}
	result, err := h.options.Installations.CompleteOAuth(r.Context(), r.URL.Query().Get("state"), r.URL.Query().Get("code"), installationContinuation(r), installationCookie(r, oauthCredentialCookie))
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "installation_connection_unavailable")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_installation_callback")
		}
		return
	}
	if result.oauthCredential != "" {
		setInstallationCookie(w, oauthCredentialCookie, result.oauthCredential, result.ExpiresAt)
		http.Redirect(w, r, result.RedirectURL, http.StatusSeeOther)
		return
	}
	clearInstallationContinuationCookie(w)
	clearInstallationCookie(w, oauthCredentialCookie)
	http.Redirect(w, r, result.RedirectURL, http.StatusSeeOther)
}

func setInstallationContinuationCookie(w http.ResponseWriter, value string, expires time.Time) {
	setInstallationCookie(w, installationContinuationCookie, value, expires)
}

func clearInstallationContinuationCookie(w http.ResponseWriter) {
	clearInstallationCookie(w, installationContinuationCookie)
}

func installationContinuation(r *http.Request) string {
	return installationCookie(r, installationContinuationCookie)
}

func setInstallationCookie(w http.ResponseWriter, name, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: installationContinuationPath, Expires: expires, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

func clearInstallationCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Path: installationContinuationPath, MaxAge: -1, Expires: time.Unix(0, 0).UTC(), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

func installationCookie(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (h apiHandler) pollEvents(w http.ResponseWriter, r *http.Request) {
	m, ok := h.machine(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "machine_bearer_unavailable")
		return
	}
	if h.options.RepositoryEvents == nil {
		writeError(w, http.StatusServiceUnavailable, "repository_event_poll_failed")
		return
	}
	q := r.URL.Query()
	limit, e := boundedInt(q.Get("limit"), repositoryevent.DefaultLimit, 1, repositoryevent.MaxLimit)
	if e != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit")
		return
	}
	wait, e := boundedInt(q.Get("wait_seconds"), repositoryevent.DefaultWaitSeconds, 0, repositoryevent.MaxWaitSeconds)
	if e != nil {
		writeError(w, http.StatusBadRequest, "invalid_wait_seconds")
		return
	}
	response, e := h.options.RepositoryEvents.Poll(r.Context(), m, q.Get("cursor"), limit, time.Duration(wait)*time.Second)
	if e != nil {
		writeError(w, http.StatusServiceUnavailable, "repository_event_poll_failed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h apiHandler) ackEvents(w http.ResponseWriter, r *http.Request) {
	m, ok := h.machine(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "machine_bearer_unavailable")
		return
	}
	if h.options.RepositoryEvents == nil {
		writeError(w, http.StatusServiceUnavailable, "repository_event_ack_failed")
		return
	}
	var body repositoryevent.AckRequest
	if decodeRequest(w, r, 64<<10, &body) != nil {
		writeError(w, http.StatusBadRequest, "invalid_acknowledgement")
		return
	}
	response, e := h.options.RepositoryEvents.Acknowledge(r.Context(), m, body)
	if e != nil {
		writeError(w, http.StatusBadRequest, "invalid_acknowledgement")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// narrateRejection writes the one line a rejected delivery gets, before the
// response to GitHub is sent. The subject is whatever repository the delivery
// has already been attributed to; an unverified payload has none, and is
// narrated against github.com rather than against a name the request itself
// supplied.
func (h apiHandler) narrateRejection(r *http.Request, repository, reason string) {
	if h.options.Narrate == nil {
		return
	}
	subject := repository
	if strings.TrimSpace(subject) == "" {
		subject = "github.com"
	}
	h.options.Narrate(narrate.Line{At: time.Now(), Event: r.Header.Get("X-GitHub-Event"), Subject: subject, Action: "rejected: " + reason})
}

func (h apiHandler) webhook(w http.ResponseWriter, r *http.Request) {
	if len(h.options.WebhookSecret) < MinimumWebhookSecretBytes || h.options.RepositoryEvents == nil {
		h.narrateRejection(r, "", "webhook not configured")
		writeError(w, http.StatusServiceUnavailable, "webhook_unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes)
	payload, e := io.ReadAll(r.Body)
	if e != nil {
		h.narrateRejection(r, "", "unreadable request body")
		writeError(w, http.StatusBadRequest, "invalid_webhook")
		return
	}
	signature := r.Header.Get("X-Hub-Signature-256")
	if !verifyWebhookSignature(h.options.WebhookSecret, payload, signature) {
		h.narrateRejection(r, "", "bad signature")
		writeError(w, http.StatusUnauthorized, "invalid_webhook_signature")
		return
	}
	delivery := WebhookDelivery{ID: r.Header.Get("X-GitHub-Delivery"), Event: r.Header.Get("X-GitHub-Event"), Payload: payload}
	var envelope struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	_ = json.Unmarshal(payload, &envelope)
	if envelope.Repository.FullName != "" {
		delivery.Repository = canonicalRepository(envelope.Repository.FullName)
	}
	if _, e := h.options.RepositoryEvents.EnqueueWebhook(r.Context(), delivery); e != nil {
		// Past signature verification, every way the service refuses a
		// delivery means the hub cannot attribute it to an installation it
		// knows: an installation it has no binding for, or a payload whose
		// installation and repository identifiers it will not trust. The
		// reason is deliberately coarse so no part of the payload reaches the
		// console.
		h.narrateRejection(r, delivery.Repository, "unknown installation")
		writeError(w, http.StatusServiceUnavailable, "webhook_event_enqueue_failed")
		return
	}
	if h.options.Projection != nil {
		if e := h.options.Projection.ProcessProjection(r.Context(), delivery, signature); e != nil {
			writeError(w, http.StatusServiceUnavailable, "webhook_projection_failed")
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func verifyWebhookSignature(secret, payload []byte, signature string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	provided, e := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if e != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return hmac.Equal(provided, mac.Sum(nil))
}

func decodeRequest(w http.ResponseWriter, r *http.Request, limit int64, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(out); e != nil {
		return e
	}
	var extra any
	if e := d.Decode(&extra); !errors.Is(e, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}

func boundedInt(raw string, fallback, min, max int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	v, e := strconv.Atoi(raw)
	if e != nil || v < min || v > max {
		return 0, errors.New("out of bounds")
	}
	return v, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func cors(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin != "" && r.Header.Get("Origin") == origin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			if r.Header.Get("Origin") != origin {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
