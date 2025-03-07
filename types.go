package main

import (
	"encoding/json"
	"image"
	"time"
)

type User struct {
	Id            string `json:"id,omitempty" mapstructure:"id,omitempty"`
	Username      string `json:"username" mapstructure:"username,omitempty"`
	Discriminator string `json:"discriminator,omitempty" mapstructure:"discriminator,omitempty"`
	Avatar        string `json:"avatar,omitempty" mapstructure:"avatar,omitempty"`
	Bot           bool   `json:"bot,omitempty" mapstructure:"bot,omitempty"`
}

var EmptyInternalState = InternalState{}

type InternalState struct {
	Talking  bool
	Joined   bool
	JoinTick int // time for showing join animation
	Left     bool
	LeftTick int // time for showing leave animation

	CachedImage *image.RGBA

	StateImage *image.RGBA
}

type VoiceState struct {
	Mute     bool `json:"mute" mapstructure:"mute"`
	Deaf     bool `json:"deaf" mapstructure:"deaf"`
	SelfMute bool `json:"self_mute" mapstructure:"self_mute"`
	SelfDeaf bool `json:"self_deaf" mapstructure:"self_deaf"`
	Suppress bool `json:"suppress" mapstructure:"suppress"`
}

type UserState struct {
	VoiceState VoiceState `json:"voice_state" mapstructure:"voice_state"`
	User       User       `json:"user" mapstructure:"user"`
	Nick       string     `json:"nick" mapstructure:"nick"`
	Volume     float64    `json:"volume" mapstructure:"volume"`
	Mute       bool       `json:"mute" mapstructure:"mute"`

	InternalState `json:"-" mapstructure:"-"`
}

type MessagePayload struct {
	Cmd CommandType `json:"cmd"`
	Evt EventType   `json:"evt,omitempty"`

	Data json.RawMessage `json:"data"`

	ResolvedData interface{} `json:"-"`
}

func (m *MessagePayload) Resolve() error {
	switch m.Cmd {
	case CommandAuthorize:
		m.ResolvedData = &AuthorizePayload{}
	case CommandAuthenticate:
		m.ResolvedData = &AuthenticatePayload{}
	case CommandGetSelectedVoiceChannel:
		m.ResolvedData = &VoiceChannel{}
	case CommandDispatch:
		switch m.Evt {
		case EventReady:
			m.ResolvedData = &ReadyInfo{}
		case EventVoiceStateCreate:
			m.ResolvedData = &UserState{}
		case EventVoiceStateDelete:
			m.ResolvedData = &UserState{}
		case EventVoiceStateUpdate:
			m.ResolvedData = &UserState{}
		case EventVoiceChannelSelect:
			m.ResolvedData = &VoiceChannel{}
		case EventSpeakingStart:
			m.ResolvedData = &UserIdInfo{}
		case EventSpeakingStop:
			m.ResolvedData = &UserIdInfo{}
		}
	}

	if m.Evt == "ERROR" {
		return nil
	}
	if m.ResolvedData != nil {
		return json.Unmarshal(m.Data, m.ResolvedData)
	}
	return nil
}

type Args struct {
	ChannelID string `json:"channel_id,omitempty"`
	GuildID   string `json:"guild_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
}

type CmdRequest struct {
	Nonce string      `json:"nonce"`
	Args  any         `json:"args"`
	Cmd   CommandType `json:"cmd"`
	Evt   EventType   `json:"evt,omitempty"`
}

type UserIdInfo struct {
	UserId string `json:"user_id"`
}

type VoiceChannel struct {
	Id          string      `json:"id,omitempty"`
	Name        string      `json:"name,omitempty"`
	Type        int         `json:"type,omitempty"`
	Bitrate     int         `json:"bitrate,omitempty"`
	UserLimit   int         `json:"user_limit,omitempty"`
	GuildId     string      `json:"guild_id,omitempty"`
	Position    int         `json:"position,omitempty"`
	VoiceStates []UserState `json:"voice_states,omitempty"`
}
type ReadyInfo struct {
	V      int `json:"v"`
	Config struct {
		CdnHost     string `json:"cdn_host"`
		ApiEndpoint string `json:"api_endpoint"`
		Environment string `json:"environment"`
	} `json:"config"`
	User User `json:"user"`
}

type CommandType string

const (
	CommandGetSelectedVoiceChannel CommandType = "GET_SELECTED_VOICE_CHANNEL"
	CommandAuthenticate            CommandType = "AUTHENTICATE"
	CommandAuthorize               CommandType = "AUTHORIZE"
	CommandDispatch                CommandType = "DISPATCH"
	CommandSubscribe               CommandType = "SUBSCRIBE"
	CommandUnsubscribe             CommandType = "UNSUBSCRIBE"
)

type EventType string

const (
	EventReady              EventType = "READY"
	EventVoiceStateCreate   EventType = "VOICE_STATE_CREATE"
	EventVoiceStateDelete   EventType = "VOICE_STATE_DELETE"
	EventVoiceStateUpdate   EventType = "VOICE_STATE_UPDATE"
	EventSpeakingStart      EventType = "SPEAKING_START"
	EventSpeakingStop       EventType = "SPEAKING_STOP"
	EventVoiceChannelSelect EventType = "VOICE_CHANNEL_SELECT"
)

type AuthorizeArgs struct {
	ClientId string   `json:"client_id"`
	Scopes   []string `json:"scopes"`
}

type AuthorizePayload struct {
	Code string `json:"code"`
}

type AuthenticateArgs struct {
	AccessToken string `json:"access_token"`
}
type Application struct {
	Description string   `json:"description"`
	Icon        string   `json:"icon"`
	Id          string   `json:"id"`
	RpcOrigins  []string `json:"rpc_origins"`
	Name        string   `json:"name"`
}

type AuthenticatePayload struct {
	Application Application `json:"application"`
	Expires     time.Time   `json:"expires"`
	User        User        `json:"user"`
	Scopes      []string    `json:"scopes"`
}
