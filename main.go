package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/globalsign/mgo"
	"github.com/globalsign/mgo/bson"
	"github.com/reconquest/karma-go"
	"github.com/reconquest/pkg/log"
	telebot "gopkg.in/telebot.v3"
)

type Watcher struct {
	bot      *telebot.Bot
	chat     *telebot.Chat
	store    *mgo.Collection
	duration time.Duration
}

func main() {
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

func (watcher *Watcher) handle(update telebot.Update) error {
	if update.Message == nil {
		return nil
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

	for update := range updates {
		watcher.handle(update)
	}

	log.Infof(nil, "telekick started")
}

func (watcher *Watcher) WatchKick() {
	interval := time.Hour

	type User struct {
		UserID int64 `bson:"user_id"`
	}
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
