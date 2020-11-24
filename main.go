package main

import (
	"bytes"
	"container/list"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/infracloudio/msbotbuilder-go/core"
	"github.com/infracloudio/msbotbuilder-go/core/activity"
	"github.com/infracloudio/msbotbuilder-go/schema"
)

var deququeChan = make(chan string)
var enququeChan = make(chan string)
var nextTurnNotificationChan = make(chan *schema.ConversationReference)
var timeoutNotificationChan = make(chan *schema.ConversationReference)

var webhook = os.Getenv("BOT_WEBHOOK")

// HTTPHandler handles the HTTP requests from then connector service
type HTTPHandler struct {
	core.Adapter
	activity.HandlerFuncs
	*ReservationQueue
	timeout int
}

func (ht *HTTPHandler) OnMessageFunc(turn *activity.TurnContext) (schema.Activity, error) {
	message := turn.Activity.Text
	answer := "I undestand following commands: lock, free, info"

	ref := &schema.ConversationReference{
		ActivityID:   turn.Activity.ID,
		User:         turn.Activity.From,
		Bot:          turn.Activity.Recipient,
		Conversation: turn.Activity.Conversation,
		ChannelID:    turn.Activity.ChannelID,
		ServiceURL:   turn.Activity.ServiceURL,
	}
	lenght := ht.Len()

	if message == "free" {
		answer = "There were no reservations for you anymore"
		if ht.findElementByConversationId(ref.Conversation.ID) != nil {
			deququeChan <- ref.Conversation.ID
			answer = "Your reservation was removed"
		}
	} else if message == "lock" {
		answer = fmt.Sprintf("%s, your reservation is arrived. %d reservations before you", ref.User.Name, lenght)
		if lenght == 0 {
			answer = fmt.Sprintf("%s, your can immediately start", ref.User.Name)
		}
		ht.enquque(ref)
	} else if message == "info" {
		answer = fmt.Sprintf("The timeout is %d seconds. There are %d reservations in the quque", ht.timeout, lenght)
		for e := ht.Front(); e != nil; e = e.Next() {
			answer += fmt.Sprintf(" ->%s", e.Value.(*schema.ConversationReference).User.Name)
		}
	}

	return turn.SendActivity(activity.MsgOptionText(answer))
}

func (ht *HTTPHandler) processMessage(w http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	act, err := ht.Adapter.ParseRequest(ctx, req)
	if err != nil {
		log.Printf("Failed to process request: %s", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err = ht.Adapter.ProcessActivity(ctx, act, ht.HandlerFuncs)
	if err != nil {
		log.Printf("Failed to process request: %s", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Println("Request processed successfully")
}

func NewHTTPHandler(adapter core.Adapter) *HTTPHandler {

	timeout, err := strconv.Atoi(os.Getenv("BOT_TIMEOUT"))

	if err != nil {
		timeout = 15
	}

	quque := NewTimedReservationQueue(timeout)

	ht := &HTTPHandler{adapter, activity.HandlerFuncs{}, quque, timeout}
	ht.HandlerFuncs.OnMessageFunc = ht.OnMessageFunc

	go func() {
		for {
			select {
			case el := <-timeoutNotificationChan:
				ht.sendNotificationToConversation(el, "you are timeouted. please make resources free")
			case el := <-nextTurnNotificationChan:

				if el == nil {
					go sendWebHook(`{"text": "dev is free now"}`)
				} else {
					go sendWebHook(`{"text": "` + el.User.Name + ` locked dev"}`)
					ht.sendNotificationToConversation(el, el.User.Name+", it's your turn!")
				}
			}
		}
	}()

	return ht
}

func sendWebHook(text string) {

	if webhook == "" {
		return
	}
	var jsonStr = []byte(text)
	req, err := http.NewRequest("POST", webhook, bytes.NewBuffer(jsonStr))
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to process request: %s", err)
		return
	}
	defer resp.Body.Close()
	log.Printf("response Status: %s", resp.Status)
}

func (ht *HTTPHandler) sendNotificationToConversation(conv *schema.ConversationReference, text string) {
	if conv != nil {
		ht.Adapter.ProactiveMessage(context.TODO(), *conv, activity.HandlerFuncs{

			OnMessageFunc: func(turn *activity.TurnContext) (schema.Activity, error) {
				return turn.SendActivity(activity.MsgOptionText(text))
			},
		})
	}
}

type ReservationQueue struct {
	*list.List
}

func NewTimedReservationQueue(timeout int) *ReservationQueue {
	reservation_quque := ReservationQueue{list.New()}

	go func() {
		duration := time.Duration(timeout) * time.Second
		timer := time.NewTimer(duration)
		if !timer.Stop() {
			<-timer.C
		}
		running := false

		for {
			select {
			case <-timer.C:
				running = false
				timeoutPop := reservation_quque.Front()

				if timeoutPop != nil {
					log.Printf("time is over for %s", timeoutPop.Value.(*schema.ConversationReference).Conversation.ID)
					reservation_quque.Remove(timeoutPop)
					timeoutNotificationChan <- timeoutPop.Value.(*schema.ConversationReference)
				}

				if reservation_quque.Len() > 0 {
					nextReservation := reservation_quque.Front()
					log.Printf("next reservation started for %s", nextReservation.Value.(*schema.ConversationReference).Conversation.ID)
					nextTurnNotificationChan <- nextReservation.Value.(*schema.ConversationReference)
					timer.Reset(duration)
					running = true
				} else {
					nextTurnNotificationChan <- nil
				}
			case id := <-deququeChan:
				element := reservation_quque.findElementByConversationId(id)

				if element != nil {
					if reservation_quque.Len() <= 1 {
						if !timer.Stop() && running {
							<-timer.C
						}
						running = false
						log.Printf("quque is empty now")
						nextTurnNotificationChan <- nil
					} else if element == reservation_quque.Front() {
						nextReservation := element.Next()
						log.Printf("next reservation started for %s", nextReservation.Value.(*schema.ConversationReference).Conversation.ID)
						nextTurnNotificationChan <- nextReservation.Value.(*schema.ConversationReference)
						timer.Reset(duration)
					}

					reservation_quque.Remove(element)
				}
			case <-enququeChan:
				if !running {
					timer.Reset(duration)
					log.Printf("start timer")
					running = true
					element := reservation_quque.Front()
					nextTurnNotificationChan <- element.Value.(*schema.ConversationReference)
				}
			}
		}
	}()

	return &reservation_quque
}

func (rq *ReservationQueue) findElementByConversationId(convId string) *list.Element {
	for e := rq.Front(); e != nil; e = e.Next() {
		foundElement := e.Value.(*schema.ConversationReference)

		if foundElement.Conversation.ID == convId {
			return e
		}
	}

	return nil
}

func (rq *ReservationQueue) enquque(conversation *schema.ConversationReference) {
	log.Printf("add %s to the reservation quque with conid %s", conversation.User.ID, conversation.Conversation.ID)

	rq.PushBack(conversation)
	enququeChan <- conversation.Conversation.ID
}

func main() {

	setting := core.AdapterSetting{
		AppID:       os.Getenv("APP_ID"),
		AppPassword: os.Getenv("APP_PASSWORD"),
	}

	adapter, err := core.NewBotAdapter(setting)
	if err != nil {
		log.Fatal("Error creating adapter: ", err)
	}

	httpHandler := NewHTTPHandler(adapter)

	http.HandleFunc("/api/messages", httpHandler.processMessage)
	log.Println("Starting server on port:3978...")
	http.ListenAndServe(":3978", nil)
}
