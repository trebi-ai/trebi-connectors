// Package fakediscord is an in-process Discord for `serve --sandbox` and
// tests: a REST API and a gateway on local httptest servers, with one
// fixed guild, two text channels, a thread, and a DM. It accepts any bot
// token and keeps everything in memory.
package fakediscord

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/trebi-ai/trebi-connectors/connectors/discord-cli/internal/client"
)

// Fixed ids of the fake. They are snowflakes of 2026-01-01.
const (
	GuildID   = "1455000000000000001"
	GeneralID = "1455000000000000010"
	TeamID    = "1455000000000000011"
	ThreadID  = "1455000000000000020"
	DMID      = "1455000000000000030"
	BotID     = "1455000000000000090"
	AnaID     = "1455000000000000091"
)

// Discord channel types that the fake uses.
const (
	typeText         = 0
	typeDM           = 1
	typePublicThread = 11
)

// discordEpoch is the start of the snowflake clock in ms.
const discordEpoch = 1420070400000

// Server is one fake Discord.
type Server struct {
	rest *httptest.Server
	gw   *httptest.Server

	mu       sync.Mutex
	seq      uint64
	users    map[string]client.User
	channels map[string]*client.Channel
	order    []string // channel ids in creation order
	messages map[string][]client.Message
	conns    map[*websocket.Conn]*sync.Mutex
	gwSeq    int
	pending  []map[string]any // dispatches while no gateway is connected
}

// Start runs the fake. Close stops it.
func Start() *Server {
	s := &Server{
		users: map[string]client.User{
			BotID: {ID: BotID, Username: "trebi-sandbox", Bot: true},
			AnaID: {ID: AnaID, Username: "ana", GlobalName: "Ana"},
		},
		channels: map[string]*client.Channel{},
		messages: map[string][]client.Message{},
		conns:    map[*websocket.Conn]*sync.Mutex{},
	}
	s.add(&client.Channel{ID: GeneralID, Type: typeText, GuildID: GuildID, Name: "general"})
	s.add(&client.Channel{ID: TeamID, Type: typeText, GuildID: GuildID, Name: "team"})
	s.add(&client.Channel{ID: ThreadID, Type: typePublicThread, GuildID: GuildID, Name: "Welcome", ParentID: GeneralID})
	s.add(&client.Channel{ID: DMID, Type: typeDM, Recipients: []client.User{s.users[AnaID]}})

	mux := http.NewServeMux()
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /users/@me":                                                   s.me,
		"GET /users/@me/guilds":                                            s.guilds,
		"POST /users/@me/channels":                                         s.openDM,
		"GET /guilds/{guild}/channels":                                     s.guildChannels,
		"GET /guilds/{guild}/threads/active":                               s.activeThreads,
		"GET /channels/{channel}":                                          s.channel,
		"GET /channels/{channel}/messages":                                 s.history,
		"POST /channels/{channel}/messages":                                s.post,
		"PATCH /channels/{channel}/messages/{message}":                     s.edit,
		"PUT /channels/{channel}/messages/{message}/reactions/{emoji}/@me": s.react,
		"POST /channels/{channel}/typing":                                  s.typing,
		"POST /channels/{channel}/threads":                                 s.createThread,
		"POST /channels/{channel}/messages/{message}/threads":              s.createThread,
		"GET /channels/{channel}/threads/archived/public":                  s.archived,
	} {
		mux.HandleFunc(pattern, h)
	}
	s.rest = httptest.NewServer(mux)
	s.gw = httptest.NewServer(http.HandlerFunc(s.gateway))
	return s
}

// URL is the REST base URL (client.Client.BaseURL).
func (s *Server) URL() string { return s.rest.URL }

// GatewayURL is the WebSocket URL of the gateway.
func (s *Server) GatewayURL() string { return "ws" + strings.TrimPrefix(s.gw.URL, "http") }

// Close stops both servers and ends each gateway connection.
func (s *Server) Close() {
	s.mu.Lock()
	for c := range s.conns {
		c.Close() //nolint:errcheck // the fake stops
	}
	s.mu.Unlock()
	s.gw.Close()
	s.rest.Close()
}

// add stores a channel. The caller holds mu, or the server is not started.
func (s *Server) add(ch *client.Channel) {
	s.channels[ch.ID] = ch
	s.order = append(s.order, ch.ID)
}

// nextID makes a snowflake of now. The caller holds mu.
func (s *Server) nextID() string {
	s.seq++
	ms := uint64(time.Now().UnixMilli() - discordEpoch)
	return strconv.FormatUint(ms<<22|s.seq&0xfff, 10)
}

func (s *Server) me(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, s.users[BotID])
}

func (s *Server) guilds(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, []client.Guild{{ID: GuildID, Name: "Sandbox"}})
}

func (s *Server) guildChannels(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("guild") != GuildID {
		unknown(w, "Unknown Guild")
		return
	}
	reply(w, http.StatusOK, s.list(func(ch *client.Channel) bool { return ch.GuildID == GuildID && ch.Type == typeText }))
}

func (s *Server) activeThreads(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("guild") != GuildID {
		unknown(w, "Unknown Guild")
		return
	}
	threads := s.list(func(ch *client.Channel) bool { return ch.Type == typePublicThread })
	reply(w, http.StatusOK, client.ActiveThreadsResponse{Threads: threads})
}

func (s *Server) archived(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.find(r.PathValue("channel")); !ok {
		unknown(w, "Unknown Channel")
		return
	}
	reply(w, http.StatusOK, client.ArchivedThreadsResponse{Threads: []client.Channel{}})
}

// list copies the channels that match, in creation order.
func (s *Server) list(match func(*client.Channel) bool) []client.Channel {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []client.Channel{}
	for _, id := range s.order {
		if ch := s.channels[id]; match(ch) {
			out = append(out, *ch)
		}
	}
	return out
}

func (s *Server) find(id string) (client.Channel, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.channels[id]
	if !ok {
		return client.Channel{}, false
	}
	return *ch, true
}

func (s *Server) channel(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.find(r.PathValue("channel"))
	if !ok {
		unknown(w, "Unknown Channel")
		return
	}
	reply(w, http.StatusOK, ch)
}

// openDM opens the DM channel with any recipient id.
func (s *Server) openDM(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RecipientID string `json:"recipient_id"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.RecipientID == "" {
		invalid(w, "recipient_id is missing")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.order {
		ch := s.channels[id]
		if ch.Type == typeDM && len(ch.Recipients) > 0 && ch.Recipients[0].ID == body.RecipientID {
			reply(w, http.StatusOK, *ch)
			return
		}
	}
	u, ok := s.users[body.RecipientID]
	if !ok {
		u = client.User{ID: body.RecipientID, Username: "user-" + body.RecipientID}
		s.users[u.ID] = u
	}
	ch := &client.Channel{ID: s.nextID(), Type: typeDM, Recipients: []client.User{u}}
	s.add(ch)
	reply(w, http.StatusOK, *ch)
}

// history returns the messages newest first, like Discord.
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("channel")
	if _, ok := s.find(id); !ok {
		unknown(w, "Unknown Channel")
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit")) //nolint:errcheck // a bad limit takes the default
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	before, _ := strconv.ParseUint(q.Get("before"), 10, 64) //nolint:errcheck // no filter on a bad id
	after, _ := strconv.ParseUint(q.Get("after"), 10, 64)   //nolint:errcheck // no filter on a bad id
	s.mu.Lock()
	var out []client.Message
	for _, m := range slices.Backward(s.messages[id]) {
		n, _ := strconv.ParseUint(m.ID, 10, 64) //nolint:errcheck // the fake makes the ids
		if (before == 0 || n < before) && n > after {
			out = append(out, m)
		}
	}
	s.mu.Unlock()
	if len(out) > limit {
		out = out[:limit]
	}
	reply(w, http.StatusOK, orEmpty(out))
}

// post creates a message from JSON or from a multipart upload and sends
// MESSAGE_CREATE on the gateway.
func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("channel")
	ch, ok := s.find(id)
	if !ok {
		unknown(w, "Unknown Channel")
		return
	}
	var body struct {
		Content          string                   `json:"content"`
		MessageReference *client.MessageReference `json:"message_reference"`
	}
	var atts []client.Attachment
	mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type")) //nolint:errcheck // an empty type is JSON
	if mt == "multipart/form-data" {
		var err error
		if atts, err = s.readParts(multipart.NewReader(r.Body, params["boundary"]), &body); err != nil {
			invalid(w, err.Error())
			return
		}
	} else if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		invalid(w, "body: "+err.Error())
		return
	}
	if body.Content == "" && len(atts) == 0 {
		invalid(w, "Cannot send an empty message")
		return
	}
	if len([]rune(body.Content)) > 2000 {
		invalid(w, "content: Must be 2000 or fewer in length.")
		return
	}
	s.mu.Lock()
	bot := s.users[BotID]
	m := client.Message{
		ID: s.nextID(), ChannelID: id, GuildID: ch.GuildID, Author: &bot, Content: body.Content,
		Timestamp: time.Now().UTC(), Attachments: atts,
	}
	if body.MessageReference != nil && body.MessageReference.MessageID != "" {
		m.Type, m.MessageReference = 19, body.MessageReference
	}
	s.messages[id] = append(s.messages[id], m)
	s.channels[id].LastMessageID = m.ID
	s.mu.Unlock()
	s.dispatch("MESSAGE_CREATE", m)
	reply(w, http.StatusOK, m)
}

// readParts reads payload_json and the files of an upload.
func (s *Server) readParts(mr *multipart.Reader, body any) ([]client.Attachment, error) {
	var atts []client.Attachment
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return atts, nil
		}
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(p)
		if err != nil {
			return nil, err
		}
		if p.FormName() == "payload_json" {
			if err := json.Unmarshal(data, body); err != nil {
				return nil, err
			}
			continue
		}
		s.mu.Lock()
		aid := s.nextID()
		s.mu.Unlock()
		atts = append(atts, client.Attachment{
			ID: aid, Filename: p.FileName(), Size: len(data),
			URL: "https://cdn.discordapp.example/attachments/" + aid + "/" + p.FileName(),
		})
	}
}

// message finds a message in a channel. The caller holds mu.
func (s *Server) message(channel, id string) (*client.Message, bool) {
	for i := range s.messages[channel] {
		if s.messages[channel][i].ID == id {
			return &s.messages[channel][i], true
		}
	}
	return nil, false
}

func (s *Server) edit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content string `json:"content"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Content == "" {
		invalid(w, "content is missing")
		return
	}
	s.mu.Lock()
	m, ok := s.message(r.PathValue("channel"), r.PathValue("message"))
	if !ok {
		s.mu.Unlock()
		unknown(w, "Unknown Message")
		return
	}
	now := time.Now().UTC()
	m.Content, m.EditedTimestamp = body.Content, &now
	out := *m
	s.mu.Unlock()
	reply(w, http.StatusOK, out)
}

// react adds the reaction of the bot and sends MESSAGE_REACTION_ADD.
func (s *Server) react(w http.ResponseWriter, r *http.Request) {
	channel, msg := r.PathValue("channel"), r.PathValue("message")
	s.mu.Lock()
	ch, okc := s.channels[channel]
	_, okm := s.message(channel, msg)
	bot := s.users[BotID]
	s.mu.Unlock()
	if !okc || !okm {
		unknown(w, "Unknown Message")
		return
	}
	emoji := client.Emoji{Name: r.PathValue("emoji")}
	if name, id, ok := strings.Cut(emoji.Name, ":"); ok {
		emoji = client.Emoji{Name: name, ID: id}
	}
	s.dispatch("MESSAGE_REACTION_ADD", map[string]any{
		"user_id": bot.ID, "channel_id": channel, "message_id": msg, "guild_id": ch.GuildID,
		"emoji": emoji, "member": map[string]any{"user": bot},
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) typing(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.find(r.PathValue("channel")); !ok {
		unknown(w, "Unknown Channel")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// createThread starts a public thread. A thread on a message has the id
// of the message, like Discord.
func (s *Server) createThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Name == "" {
		invalid(w, "name is missing")
		return
	}
	parent := r.PathValue("channel")
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.channels[parent]
	if !ok {
		unknown(w, "Unknown Channel")
		return
	}
	if ch.Type != typeText {
		invalid(w, "Cannot create a thread in this channel")
		return
	}
	id := r.PathValue("message")
	if id != "" {
		if _, ok := s.message(parent, id); !ok {
			unknown(w, "Unknown Message")
			return
		}
		if _, ok := s.channels[id]; ok {
			invalid(w, "A thread has already been created for this message")
			return
		}
	} else {
		id = s.nextID()
	}
	th := &client.Channel{ID: id, Type: typePublicThread, GuildID: ch.GuildID, Name: body.Name, ParentID: parent}
	s.add(th)
	reply(w, http.StatusCreated, *th)
}

// gateway runs HELLO, IDENTIFY, READY, and GUILD_CREATE, then keeps the
// connection for dispatches and answers heartbeats.
func (s *Server) gateway(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close() //nolint:errcheck // the connection ends
	wmu := &sync.Mutex{}
	write := func(v any) error {
		wmu.Lock()
		defer wmu.Unlock()
		return conn.WriteJSON(v)
	}
	if write(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 41250}}) != nil {
		return
	}
	for {
		var p struct {
			Op int `json:"op"`
		}
		if conn.ReadJSON(&p) != nil {
			return
		}
		if p.Op == 2 {
			break
		}
	}
	s.mu.Lock()
	bot := s.users[BotID]
	var channels, threads []client.Channel
	for _, id := range s.order {
		switch ch := s.channels[id]; {
		case ch.GuildID == GuildID && ch.Type == typeText:
			channels = append(channels, *ch)
		case ch.GuildID == GuildID && ch.Type == typePublicThread:
			threads = append(threads, *ch)
		}
	}
	s.gwSeq++
	ready := map[string]any{"op": 0, "t": "READY", "s": s.gwSeq, "d": map[string]any{
		"v": 10, "session_id": "sandbox", "user": bot,
		"guilds": []any{map[string]any{"id": GuildID, "unavailable": true}},
	}}
	s.gwSeq++
	create := map[string]any{"op": 0, "t": "GUILD_CREATE", "s": s.gwSeq, "d": map[string]any{
		"id": GuildID, "name": "Sandbox", "channels": channels, "threads": threads,
	}}
	s.mu.Unlock()
	if write(ready) != nil || write(create) != nil {
		return
	}
	s.mu.Lock()
	s.conns[conn] = wmu
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	for _, p := range pending {
		if write(p) != nil {
			return
		}
	}
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()
	for {
		var p struct {
			Op int `json:"op"`
		}
		if conn.ReadJSON(&p) != nil {
			return
		}
		if p.Op == 1 {
			if write(map[string]any{"op": 11}) != nil {
				return
			}
		}
	}
}

// dispatch sends one event to each gateway connection. With none, the
// next connection gets it, so a send right after the start still gives
// its event.
func (s *Server) dispatch(t string, d any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gwSeq++
	p := map[string]any{"op": 0, "t": t, "s": s.gwSeq, "d": d}
	if len(s.conns) == 0 {
		s.pending = append(s.pending, p)
		return
	}
	for c, wmu := range s.conns {
		wmu.Lock()
		c.WriteJSON(p) //nolint:errcheck // a dead connection ends in its read loop
		wmu.Unlock()
	}
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck // the client went away
}

func unknown(w http.ResponseWriter, msg string) {
	reply(w, http.StatusNotFound, map[string]any{"message": msg, "code": 10003})
}

func invalid(w http.ResponseWriter, msg string) {
	reply(w, http.StatusBadRequest, map[string]any{"message": msg, "code": 50035})
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
