package telegram

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gotd/td/constant"
	"github.com/gotd/td/session"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query/channels/participants"
	"github.com/gotd/td/tg"
)

var ErrMTProtoUnavailable = errors.New("Telegram MTProto API is not configured")

type TelegramMember struct {
	ID        int64
	FirstName string
	LastName  string
	Username  string
	Bot       bool
	Deleted   bool
}

// MTProto keeps a single authenticated bot connection alive for current group roster queries.
type MTProto struct {
	apiID     int
	apiHash   string
	botToken  string
	client    *gotdtelegram.Client
	manager   *peers.Manager
	ready     chan struct{}
	readyErr  error
	readyMu   sync.RWMutex
	once      sync.Once
	channel   peers.Channel
	channelMu sync.RWMutex
}

func NewMTProto(apiID int, apiHash, botToken, sessionPath string) (*MTProto, error) {
	if apiID == 0 || apiHash == "" {
		return nil, nil
	}
	if botToken == "" {
		return nil, fmt.Errorf("MTProto bot authentication: TELEGRAM_BOT_TOKEN is empty")
	}
	client := gotdtelegram.NewClient(apiID, apiHash, gotdtelegram.Options{SessionStorage: &session.FileStorage{Path: sessionPath}})
	return &MTProto{apiID: apiID, apiHash: apiHash, botToken: botToken, client: client, manager: peers.Options{}.Build(client.API()), ready: make(chan struct{})}, nil
}

func (c *MTProto) Start(ctx context.Context) error {
	go func() {
		err := c.client.Run(ctx, func(ctx context.Context) error {
			if _, err := c.client.Auth().Bot(ctx, c.botToken); err != nil {
				return fmt.Errorf("bot authentication: %w", err)
			}
			if err := c.manager.Init(ctx); err != nil {
				return fmt.Errorf("initialize peers: %w", err)
			}
			c.markReady(nil)
			<-ctx.Done()
			return ctx.Err()
		})
		c.markReady(err)
	}()
	select {
	case <-c.ready:
		c.readyMu.RLock()
		err := c.readyErr
		c.readyMu.RUnlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *MTProto) markReady(err error) {
	c.once.Do(func() {
		c.readyMu.Lock()
		c.readyErr = err
		c.readyMu.Unlock()
		close(c.ready)
	})
}

func (c *MTProto) Members(ctx context.Context, chatID int64) ([]TelegramMember, error) {
	if c == nil {
		return nil, ErrMTProtoUnavailable
	}
	select {
	case <-c.ready:
		c.readyMu.RLock()
		err := c.readyErr
		c.readyMu.RUnlock()
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	channel, err := c.resolveChannel(ctx, chatID)
	if err != nil {
		return nil, err
	}
	var members []TelegramMember
	err = participants.NewQueryBuilder(c.client.API()).GetParticipants(channel.InputChannel()).Recent().ForEach(ctx, func(_ context.Context, elem participants.Elem) error {
		user, ok := elem.User()
		if !ok {
			return nil
		}
		username, _ := user.GetUsername()
		members = append(members, TelegramMember{ID: user.ID, FirstName: user.FirstName, LastName: user.LastName, Username: username, Bot: user.Bot, Deleted: user.Deleted})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("channels.getParticipants: %w", err)
	}
	return members, nil
}

func (c *MTProto) resolveChannel(ctx context.Context, chatID int64) (peers.Channel, error) {
	c.channelMu.RLock()
	channel := c.channel
	c.channelMu.RUnlock()
	if channel.ID() != 0 {
		return channel, nil
	}
	dialogs, err := c.client.API().MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{Limit: 100, OffsetPeer: &tg.InputPeerEmpty{}})
	if err != nil {
		return peers.Channel{}, fmt.Errorf("load Telegram dialogs: %w", err)
	}
	var users []tg.UserClass
	var chats []tg.ChatClass
	switch dialogs := dialogs.(type) {
	case *tg.MessagesDialogs:
		users, chats = dialogs.Users, dialogs.Chats
	case *tg.MessagesDialogsSlice:
		users, chats = dialogs.Users, dialogs.Chats
	default:
		return peers.Channel{}, fmt.Errorf("configured group %d is absent from Telegram dialogs", chatID)
	}
	if err := c.manager.Apply(ctx, users, chats); err != nil {
		return peers.Channel{}, fmt.Errorf("cache Telegram dialogs: %w", err)
	}
	peer, err := c.manager.ResolveTDLibID(ctx, constant.TDLibPeerID(chatID))
	if err != nil {
		return peers.Channel{}, fmt.Errorf("resolve configured group %d: %w", chatID, err)
	}
	channel, ok := peer.(peers.Channel)
	if !ok {
		return peers.Channel{}, fmt.Errorf("configured group %d is not a supergroup", chatID)
	}
	c.channelMu.Lock()
	c.channel = channel
	c.channelMu.Unlock()
	return channel, nil
}
