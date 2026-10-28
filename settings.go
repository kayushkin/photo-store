package photostore

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/kayushkin/llm-bridge/msg"
	"github.com/kayushkin/llm-bridge/servicesettings"
)

// ServiceName is this service's name in its own settings description, as
// healthcheck and the repo know it.
const ServiceName = "photo-store"

// OwnedEnvironmentVariablePrefix is the prefix of the variables that are this
// service's alone. A set variable carrying it that SettingDefinitions does not
// declare stops the service from starting.
const OwnedEnvironmentVariablePrefix = "PHOTO_STORE_"

// Keys of the settings, as GET /settings names them.
const (
	SettingListenAddress = "listen_address"
	SettingDataDirectory = "data_directory"
)

// DefaultListenAddress is loopback, deliberately: these are the user's private
// photos and the service has no auth of its own. dash is the front door.
const DefaultListenAddress = "127.0.0.1:8329"

// DefaultDataDir is where the database and thumbnails live when the
// environment says nothing.
func DefaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "photo-store")
}

// SettingDefinitions declares every environment variable this process reads,
// once. Nothing is Editable: GET /settings is as open as every other route.
func SettingDefinitions() []servicesettings.Definition {
	return []servicesettings.Definition{
		{Key: SettingListenAddress, EnvironmentVariable: "PHOTO_STORE_ADDR", Kind: msg.ServiceSettingKindWiring, ValueType: msg.ServiceSettingValueTypeString, Default: DefaultListenAddress,
			Description: "The address the HTTP server listens on. Keep it on loopback: the service has no auth."},
		{Key: SettingDataDirectory, EnvironmentVariable: "PHOTO_STORE_DATA_DIR", Kind: msg.ServiceSettingKindPath, ValueType: msg.ServiceSettingValueTypeString, Default: DefaultDataDir(),
			Description: "The directory that holds photo-store.db and thumbnails/. Changing it starts the service on whatever is there, or nothing."},
	}
}

// NewSettingsRegistry reads this service's settings from environment.
func NewSettingsRegistry(environment servicesettings.Environment) (*servicesettings.Registry, error) {
	return servicesettings.New(ServiceName, []string{OwnedEnvironmentVariablePrefix}, SettingDefinitions(), environment)
}

// RegisterSettingsHandler serves the registry at GET /settings.
func RegisterSettingsHandler(mux *http.ServeMux, registry *servicesettings.Registry) {
	mux.Handle("GET /settings", servicesettings.Handler(registry, "/settings"))
}

// Keys of photo-import-messages' settings: where multichat answers and its
// bearer token. The command reads them from the environment; the service
// itself reads neither.
const (
	SettingMultichatURL    = "multichat_url"
	SettingMultichatToken  = "multichat_token"
	SettingJournalStoreURL = "journal_store_url"
)

// ImportMessagesSettingDefinitions declares every environment variable
// photo-import-messages reads.
func ImportMessagesSettingDefinitions() []servicesettings.Definition {
	return []servicesettings.Definition{
		{Key: SettingMultichatURL, EnvironmentVariable: "MULTICHAT_URL", Kind: msg.ServiceSettingKindWiring, ValueType: msg.ServiceSettingValueTypeString, Default: "http://127.0.0.1:8402",
			Description: "Where multichat answers: the chats and the message log the photos are read from."},
		{Key: SettingMultichatToken, EnvironmentVariable: "MULTICHAT_TOKEN", Kind: msg.ServiceSettingKindSecret, ValueType: msg.ServiceSettingValueTypeString, Required: true,
			Description: "multichat's bearer token."},
		{Key: SettingJournalStoreURL, EnvironmentVariable: "JOURNAL_STORE_URL", Kind: msg.ServiceSettingKindWiring, ValueType: msg.ServiceSettingValueTypeString, Default: "http://127.0.0.1:8322",
			Description: "Where journal-store answers: the transcripts a photo from a Discord DM or Messenger export is cited by."},
	}
}

// ImportFacebookSettingDefinitions declares every environment variable
// photo-import-facebook reads.
func ImportFacebookSettingDefinitions() []servicesettings.Definition {
	return []servicesettings.Definition{
		{Key: SettingJournalStoreURL, EnvironmentVariable: "JOURNAL_STORE_URL", Kind: msg.ServiceSettingKindWiring, ValueType: msg.ServiceSettingValueTypeString, Default: "http://127.0.0.1:8322",
			Description: "Where journal-store answers: the Facebook posts a photo is cited by."},
	}
}

// NewImportFacebookSettingsRegistry reads photo-import-facebook's settings.
func NewImportFacebookSettingsRegistry(environment servicesettings.Environment) (*servicesettings.Registry, error) {
	return servicesettings.New("photo-import-facebook", nil, ImportFacebookSettingDefinitions(), environment)
}

// Keys only photo-match-people reads: where people-store answers, and where
// llm-bridge-server answers the model check's POST /oneshot, with its token.
const (
	SettingPeopleStoreURL     = "people_store_url"
	SettingBridgeURL          = "llm_bridge_url"
	SettingBridgeServiceToken = "llm_bridge_service_token"
)

// MatchPeopleSettingDefinitions declares every environment variable
// photo-match-people reads.
func MatchPeopleSettingDefinitions() []servicesettings.Definition {
	return []servicesettings.Definition{
		{Key: SettingMultichatURL, EnvironmentVariable: "MULTICHAT_URL", Kind: msg.ServiceSettingKindWiring, ValueType: msg.ServiceSettingValueTypeString, Default: "http://127.0.0.1:8402",
			Description: "Where multichat answers: the chat a photo was sent in, and its members."},
		{Key: SettingMultichatToken, EnvironmentVariable: "MULTICHAT_TOKEN", Kind: msg.ServiceSettingKindSecret, ValueType: msg.ServiceSettingValueTypeString, Required: true,
			Description: "multichat's bearer token."},
		{Key: SettingJournalStoreURL, EnvironmentVariable: "JOURNAL_STORE_URL", Kind: msg.ServiceSettingKindWiring, ValueType: msg.ServiceSettingValueTypeString, Default: "http://127.0.0.1:8322",
			Description: "Where journal-store answers: the transcripts that cite a photo, and the people they link."},
		{Key: SettingPeopleStoreURL, EnvironmentVariable: "PEOPLE_STORE_URL", Kind: msg.ServiceSettingKindWiring, ValueType: msg.ServiceSettingValueTypeString, Default: "http://127.0.0.1:8321",
			Description: "Where people-store answers: which person a chat handle belongs to."},
		{Key: SettingBridgeURL, EnvironmentVariable: "LLM_BRIDGE_URL", Kind: msg.ServiceSettingKindWiring, ValueType: msg.ServiceSettingValueTypeString, Default: "http://127.0.0.1:8160",
			Description: "Where llm-bridge-server answers the model check's POST /oneshot."},
		{Key: SettingBridgeServiceToken, EnvironmentVariable: "LLMBRIDGE_SERVICE_TOKEN", Kind: msg.ServiceSettingKindSecret, ValueType: msg.ServiceSettingValueTypeString,
			Description: "llm-bridge-server's service token; needed only with -model-check."},
	}
}

// NewMatchPeopleSettingsRegistry reads photo-match-people's settings.
func NewMatchPeopleSettingsRegistry(environment servicesettings.Environment) (*servicesettings.Registry, error) {
	return servicesettings.New("photo-match-people", nil, MatchPeopleSettingDefinitions(), environment)
}

// NewImportMessagesSettingsRegistry reads the command's settings. It owns no
// prefix, so only CheckRequired can refuse.
func NewImportMessagesSettingsRegistry(environment servicesettings.Environment) (*servicesettings.Registry, error) {
	return servicesettings.New("photo-import-messages", nil, ImportMessagesSettingDefinitions(), environment)
}
