// Package config loads gateway settings from the environment and the identity registry file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Hansika-hub/anubandh/gateway/internal/fabric"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
)

// Config is the gateway configuration. Every field has an env var (README.md).
type Config struct {
	Addr               string
	PGDSN              string
	FabricMock         bool
	MockStatePath      string
	Channel            string
	Chaincode          string
	UsersFile          string
	MigrationsDir      string
	MigrateOnStart     bool
	SimBankURL         string
	BankSettleOnAccept bool
	OutboxPoll         time.Duration
	OutboxMaxAttempts  int
	TickInterval       time.Duration
	ListenerID         string
	ListenerStartBlock uint64
	CORSOrigins        []string
	InternalToken      string
	AutoRelease        bool
	LogLevel           string
}

// FromEnv reads the configuration.
func FromEnv() (Config, error) {
	c := Config{
		Addr:          env("GATEWAY_ADDR", ":8080"),
		PGDSN:         os.Getenv("PG_DSN"),
		MockStatePath: os.Getenv("FABRIC_MOCK_STATE"),
		Channel:       env("FABRIC_CHANNEL", "anubandh-ch"),
		Chaincode:     env("FABRIC_CC_NAME", "deal"),
		UsersFile:     env("GATEWAY_USERS_FILE", "config/users.json"),
		MigrationsDir: env("MIGRATIONS_DIR", "../db/migrations"),
		SimBankURL:    env("SIM_BANK_URL", "http://sim-bank:8101"),
		ListenerID:    env("LISTENER_ID", "gateway"),
		InternalToken: os.Getenv("INTERNAL_TOKEN"),
		LogLevel:      env("LOG_LEVEL", "info"),
		CORSOrigins:   splitList(env("CORS_ORIGINS", "http://localhost:3000")),
	}
	var err error
	var errs []error
	c.FabricMock, err = envBool("FABRIC_MOCK", false)
	errs = append(errs, err)
	c.MigrateOnStart, err = envBool("MIGRATE_ON_START", true)
	errs = append(errs, err)
	c.BankSettleOnAccept, err = envBool("BANK_SETTLE_ON_ACCEPT", false)
	errs = append(errs, err)
	c.AutoRelease, err = envBool("AUTO_RELEASE", true)
	errs = append(errs, err)
	c.OutboxPoll, err = envDuration("OUTBOX_POLL_INTERVAL", time.Second)
	errs = append(errs, err)
	c.TickInterval, err = envDuration("TICK_INTERVAL", time.Minute)
	errs = append(errs, err)
	c.OutboxMaxAttempts, err = envInt("OUTBOX_MAX_ATTEMPTS", 10)
	errs = append(errs, err)
	sb, err := envInt("LISTENER_START_BLOCK", 0)
	errs = append(errs, err)
	if sb < 0 {
		errs = append(errs, errors.New("LISTENER_START_BLOCK must be >= 0"))
	}
	c.ListenerStartBlock = uint64(max(sb, 0))
	if c.PGDSN == "" {
		errs = append(errs, errors.New("PG_DSN is required"))
	}
	return c, errors.Join(errs...)
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s: %q is not a boolean", k, v)
	}
	return b, nil
}

func envInt(k string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def, nil
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def, fmt.Errorf("%s: %q is not an integer", k, v)
	}
	return i, nil
}

func envDuration(k string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def, fmt.Errorf("%s: %q is not a positive duration (e.g. 30s, 1m)", k, v)
	}
	return d, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// User is one entry of the identity registry.
type User struct {
	UserID      string     `json:"userId"`
	Org         model.Org  `json:"org"`
	Role        model.Role `json:"role"`
	PartyID     string     `json:"partyId,omitempty"`
	DisplayName string     `json:"displayName,omitempty"`
	Cert        string     `json:"cert,omitempty"`
	Key         string     `json:"key,omitempty"`
}

// ServiceUsers names the identities the gateway itself signs with.
type ServiceUsers struct {
	Listener    string            `json:"listener"`
	Reader      string            `json:"reader"`
	Scheduler   string            `json:"scheduler"`
	Bank        string            `json:"bank"`
	AutoRelease string            `json:"autoRelease"`
	Internal    map[string]string `json:"internal"` // chaincode fn -> userId for /internal/v1/submit
}

type registryFile struct {
	Orgs    map[model.Org]fabric.OrgEndpoint `json:"orgs"`
	Users   []User                           `json:"users"`
	Service ServiceUsers                     `json:"service"`
}

// Registry maps web users (X-User) to enrolled chain identities.
type Registry struct {
	Orgs    map[model.Org]fabric.OrgEndpoint
	Service ServiceUsers
	users   map[string]fabric.Identity
	order   []string
}

// LoadRegistry reads and validates the users file. ${VAR} references are expanded from the env.
// requireCrypto demands cert/key paths for every user (real Drunix mode).
func LoadRegistry(path string, requireCrypto bool) (*Registry, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied config path (GATEWAY_USERS_FILE), not user input
	if err != nil {
		return nil, fmt.Errorf("users file: %w", err)
	}
	var f registryFile
	dec := json.NewDecoder(strings.NewReader(os.ExpandEnv(string(b))))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("users file %s: %w", path, err)
	}
	r := &Registry{Orgs: f.Orgs, Service: f.Service, users: map[string]fabric.Identity{}}
	for _, u := range f.Users {
		switch {
		case u.UserID == "":
			return nil, errors.New("users file: a user has no userId")
		case !u.Org.Valid():
			return nil, fmt.Errorf("users file: %s has invalid org %q", u.UserID, u.Org)
		case !u.Role.Valid():
			return nil, fmt.Errorf("users file: %s has invalid role %q", u.UserID, u.Role)
		case requireCrypto && (u.Cert == "" || u.Key == ""):
			return nil, fmt.Errorf("users file: %s needs cert and key paths when FABRIC_MOCK=false", u.UserID)
		}
		if _, dup := r.users[u.UserID]; dup {
			return nil, fmt.Errorf("users file: duplicate userId %s", u.UserID)
		}
		msp := ""
		if ep, ok := f.Orgs[u.Org]; ok {
			msp = ep.MSPID
		}
		r.users[u.UserID] = fabric.Identity{UserID: u.UserID, Org: u.Org, Role: u.Role, PartyID: u.PartyID, MSPID: msp, CertPath: u.Cert, KeyPath: u.Key}
		r.order = append(r.order, u.UserID)
	}
	svc := map[string]string{"listener": f.Service.Listener, "reader": f.Service.Reader, "scheduler": f.Service.Scheduler,
		"bank": f.Service.Bank, "autoRelease": f.Service.AutoRelease}
	for k, v := range f.Service.Internal {
		svc["internal."+k] = v
	}
	for role, uid := range svc {
		if uid == "" {
			return nil, fmt.Errorf("users file: service.%s is not set", role)
		}
		if _, ok := r.users[uid]; !ok {
			return nil, fmt.Errorf("users file: service.%s refers to unknown user %s", role, uid)
		}
	}
	if r.users[f.Service.Bank].Org != model.OrgBank {
		return nil, errors.New("users file: service.bank must be a BANK user")
	}
	return r, nil
}

// User returns the identity for a userId.
func (r *Registry) User(userID string) (fabric.Identity, bool) {
	id, ok := r.users[userID]
	return id, ok
}

// MustService returns a service identity (validated at load time).
func (r *Registry) MustService(userID string) fabric.Identity {
	id, ok := r.users[userID]
	if !ok {
		panic("config: unknown service user " + userID)
	}
	return id
}

// Users returns all identities in file order.
func (r *Registry) Users() []fabric.Identity {
	out := make([]fabric.Identity, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.users[id])
	}
	return out
}
