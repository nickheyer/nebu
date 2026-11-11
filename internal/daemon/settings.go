package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/pkg/logger"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/transfer"
	"google.golang.org/protobuf/proto"
)

// What a config file gives the settings the web UI leaves unset, sign in left out while authentication is off
func settingsBase(cfg *v1.Config) (*v1.Settings, error) {
	tr := cfg.GetTransfer()
	base := &v1.Settings{
		LogLevel:      logger.LevelName(logger.ParseLevel(cfg.GetLogging().GetLevel())),
		Downloads:     &v1.Downloads{Connections: max(tr.GetWorkers(), 1), Retries: max(tr.GetRetries(), 1), MaxBytesPerSecond: tr.GetMaxBytesPerSecond(), Windows: tr.GetWindows()},
		Storage:       &v1.DiskLimits{MaxBytes: cfg.GetStore().GetMaxBytes(), MinFreeBytes: cfg.GetMinFreeBytes()},
		Notifications: &v1.Notifications{Webhooks: fileWebhooks(cfg.GetNotify().GetWebhooks())},
	}
	if a := cfg.GetAuth(); !a.GetDisabled() {
		ttl, err := time.ParseDuration(a.GetSessionTtl())
		if err != nil || ttl <= 0 {
			return nil, fmt.Errorf("auth.session_ttl %q must be a positive duration such as 24h", a.GetSessionTtl())
		}
		base.SignIn = &v1.SignIn{SessionSeconds: uint32(ttl / time.Second), TrustedProxies: a.GetTrustedProxies()}
	}
	return proto.Clone(base).(*v1.Settings), nil
}

// The config file's webhook URLs as webhooks told of failed models in the event document, as the file has always meant them
func fileWebhooks(urls []string) []*v1.Webhook {
	var out []*v1.Webhook
	names := map[string]int{}
	for _, raw := range urls {
		name := raw
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			name = u.Host
		}
		if names[name]++; names[name] > 1 {
			name = fmt.Sprintf("%s %d", name, names[name])
		}
		sum := sha256.Sum256([]byte(raw))
		out = append(out, &v1.Webhook{
			Id: "file-" + hex.EncodeToString(sum[:6]), Name: name, Url: raw, Enabled: true,
			Format: v1.WebhookFormat_WEBHOOK_FORMAT_EVENT, Events: []v1.NotifyEvent{v1.NotifyEvent_NOTIFY_EVENT_MODEL_FAILED},
		})
	}
	return out
}

// The parts of the settings in force that must parse before any of them takes hold
type settingsPlan struct {
	schedule *transfer.Schedule
	proxies  auth.Proxies
}

func planSettings(s *v1.Settings) (settingsPlan, error) {
	var plan settingsPlan
	var err error
	d := s.GetDownloads()
	if plan.schedule, err = transfer.NewSchedule(d.GetMaxBytesPerSecond(), d.GetWindows()); err != nil {
		return plan, fmt.Errorf("download windows: %w", err)
	}
	if plan.proxies, err = auth.ParseProxies(s.GetSignIn().GetTrustedProxies()); err != nil {
		return plan, err
	}
	return plan, nil
}

// Refuses settings that do not parse, and sign in settings while authentication is off
func (d *Daemon) checkSettings(s *v1.Settings) error {
	if s.GetSignIn() != nil && d.Sessions == nil {
		return errors.New("authentication is off, so no sign in setting applies")
	}
	_, err := planSettings(s)
	return err
}

// Puts the settings in force into the logger, the downloader, the store, the notifier and the sessions
func (d *Daemon) applySettings(s *v1.Settings) error {
	plan, err := planSettings(s)
	if err != nil {
		return err
	}
	d.LogLevel.Set(logger.ParseLevel(s.GetLogLevel()))
	dl := s.GetDownloads()
	d.Puller.Fetcher.Tune(int(dl.GetConnections()), int(dl.GetRetries()), plan.schedule)
	d.Store.MaxBytes.Store(s.GetStorage().GetMaxBytes())
	d.Puller.MinFree.Store(s.GetStorage().GetMinFreeBytes())
	d.Doctor.MinFree.Store(s.GetStorage().GetMinFreeBytes())
	d.Notifier.Set(s.GetNotifications().GetWebhooks())
	if d.Sessions != nil && s.GetSignIn() != nil {
		d.Sessions.SetTTL(time.Duration(s.GetSignIn().GetSessionSeconds()) * time.Second)
		d.Sessions.SetProxies(plan.proxies)
	}
	return nil
}

// The name the host goes by, its label or else its hostname
func (d *Daemon) hostLabel() string {
	if label := d.Settings.Get().GetHostLabel(); label != "" {
		return label
	}
	name, _ := os.Hostname()
	return name
}
