package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docopt/docopt-go"
	"github.com/globalsign/mgo"
	"github.com/globalsign/mgo/bson"
	"github.com/reconquest/karma-go"
	"github.com/reconquest/pkg/log"
	telebot "gopkg.in/telebot.v3"
)

type User struct {
	UserID      int64 `bson:"user_id"`
	LastMessage int64 `bson:"last_message"`
}

var (
	version = "[manual build]"
	usage   = "telekick " + version + `

Usage:
  telekick [options]
  telekick -h | --help
  telekick --version

Options:
  -S --stats  Show stats.
  -h --help   Show this screen.
  --version   Show version.
`
)

type Watcher struct {
	bot      *telebot.Bot
	chat     *telebot.Chat
	store    *mgo.Collection
	duration time.Duration
}

func main() {
	args, err := docopt.Parse(usage, nil, true, version, false)
	if err != nil {
		panic(err)
	}

	var (
		telegramToken = stringEnv("TELEGRAM_TOKEN")
		telegramChat  = intEnv("TELEGRAM_CHAT")
		duration      = durationEnv("DURATION")

		mongoURI = stringEnv("MONGODB_URI")
	)

	bot, err := telebot.NewBot(telebot.Settings{
		Token:  telegramToken,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		log.Fatalf(err, "telegram bot init")
	}

	mongoSession, err := mgo.Dial(mongoURI)
	if err != nil {
		log.Fatal(err, "mongo dial")
	}

	store := mongoSession.DB("").C("chat")

	watcher := &Watcher{
		bot:      bot,
		chat:     &telebot.Chat{ID: int64(telegramChat)},
		store:    store,
		duration: duration,
	}

	if mode, _ := args["--stats"].(bool); mode {
		entries, err := watcher.listTimestamps()
		if err != nil {
			log.Fatal(err)
		}

		fmt.Print(entries)
		return
	}

	go watcher.Record()
	go watcher.WatchKick()

	log.Infof(nil, "telekick started")

	signals := make(chan os.Signal, 1)
	signal.Notify(
		signals,
		syscall.SIGINT,
		syscall.SIGTERM,
		os.Interrupt,
		os.Kill,
	)
	<-signals
}

func (watcher *Watcher) listTimestamps() (string, error) {
	var users []User
	err := watcher.store.Find(bson.M{}).Sort("last_message").All(&users)
	if err != nil {
		return "", err
	}

	entries := []string{}
	for _, user := range users {
		chat, err := watcher.bot.ChatByID(user.UserID)
		if err != nil {
			log.Errorf(err, "chat by id: %v", user.UserID)
			continue
		}

		timestamp := time.Unix(user.LastMessage, 0)

		entries = append(
			entries,
			"@"+chat.Username+" "+
				chat.FirstName+" "+
				chat.LastName+" "+
				time.Now().Sub(timestamp).String(),
		)
	}

	return strings.Join(entries, "\n"), nil
}

func (watcher *Watcher) handle(update telebot.Update) error {
	if update.Message == nil {
		return nil
	}

	if update.Message.Text == "/when" || update.Message.Text == "q" {
		entries, err := watcher.listTimestamps()
		if err != nil {
			return err
		}

		_, err = watcher.bot.Send(update.Message.Sender, entries)
		return err
	}

	if update.Message.UserLeft != nil {
		log.Infof(nil, "remove user: %v now: %v", update.Message.UserLeft.ID)

		err := watcher.store.Remove(
			bson.M{"user_id": update.Message.UserLeft.ID},
		)
		if err != nil {
			return karma.Format(err, "remove user")
		}

		return nil
	}

	if update.Message.UserJoined != nil {
		return watcher.updateLastMessage(update.Message.UserJoined.ID)
	}

	if update.Message.Sender == nil {
		return nil
	}

	return watcher.updateLastMessage(update.Message.Sender.ID)
}

func (watcher *Watcher) updateLastMessage(user int64) error {
	now := time.Now().Unix()

	log.Infof(nil, "update user: %v now: %v", user, now)

	_, err := watcher.store.Upsert(
		bson.M{"user_id": user},
		bson.M{
			"$set": bson.M{
				"user_id":      user,
				"last_message": now,
			},
		},
	)
	if err != nil {
		return karma.Format(err, "update user")
	}

	return nil
}

func (watcher *Watcher) Record() {
	updates := make(chan telebot.Update)
	stop := make(chan struct{})

	go watcher.bot.Poller.Poll(watcher.bot, updates, stop)

	err := watcher.bot.SetCommands(
		telebot.Command{
			Text:        "/when",
			Description: "Show the list of users and number of hours since their last message",
		},
	)
	if err != nil {
		log.Fatalf(err, "set commands")
	}

	for update := range updates {
		watcher.handle(update)
	}

	log.Infof(nil, "telekick started")
}

func (watcher *Watcher) WatchKick() {
	interval := time.Hour

	for {
		since, err := watcher.store.Find(bson.M{
			"last_message": bson.M{
				"$gt": time.Now().Add(watcher.duration * -1).Unix(),
			},
		}).Count()
		if err != nil {
			log.Fatalf(err, "find messages")
		}

		if since == 0 {
			log.Infof(nil, "no messages since %v", watcher.duration)
			time.Sleep(interval)
			continue
		}

		var users []User
		err = watcher.store.Find(bson.M{
			"last_message": bson.M{
				"$lt": time.Now().Add(watcher.duration * -1).Unix(),
			},
		}).All(&users)

		for _, user := range users {
			log.Infof(nil, "kick %v", user.UserID)

			err = watcher.ban(user.UserID)
			if err != nil {
				log.Errorf(err, "ban %v", user.UserID)
			}
		}

		time.Sleep(interval)
	}
}

func (watcher *Watcher) ban(user int64) error {
	params := map[string]string{
		"chat_id":    watcher.chat.Recipient(),
		"user_id":    strconv.FormatInt(user, 10),
		"until_date": strconv.FormatInt(telebot.Forever(), 10),
	}

	data, banErr := watcher.bot.Raw("banChatMember", params)
	if banErr != nil {
		migrations := struct {
			Parameters struct {
				MigrateToChatID int64 `json:"migrate_to_chat_id"`
			} `json:"parameters"`
		}{}

		err := json.Unmarshal(data, &migrations)
		if err != nil {
			return banErr
		}

		if migrations.Parameters.MigrateToChatID != 0 {
			params := map[string]string{
				"chat_id":    fmt.Sprint(migrations.Parameters.MigrateToChatID),
				"user_id":    strconv.FormatInt(user, 10),
				"until_date": strconv.FormatInt(telebot.Forever(), 10),
			}

			_, banErr = watcher.bot.Raw("banChatMember", params)
			return banErr
		}
	}

	return nil
}
