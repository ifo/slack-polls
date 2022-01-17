package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/slack-go/slack"
)

func main() {
	var (
		port          string
		apiKey        string
		signingSecret string
		isSet         bool
	)

	if port, isSet = os.LookupEnv("PORT"); !isSet {
		port = "3000"
	}

	if apiKey, isSet = os.LookupEnv("API_KEY"); !isSet {
		log.Fatalln("No API_KEY set")
	}

	if signingSecret, isSet = os.LookupEnv("SIGNING_SECRET"); !isSet {
		log.Fatalln("No SIGNING_SECRET set")
	}

	config := Config{
		Client:        slack.New(apiKey),
		SigningSecret: signingSecret,
	}

	http.HandleFunc("/slash", config.SlashHandler)
	http.HandleFunc("/modal", config.ModalHandler)

	log.Printf("starting server on port %s\n", port)
	log.Fatalln(http.ListenAndServe(":"+port, nil))
}

type Config struct {
	Client        *slack.Client
	SigningSecret string
}

func (c *Config) SlashHandler(w http.ResponseWriter, r *http.Request) {
	err := c.verifySigningSecret(r)
	if err != nil {
		log.Printf("Error verifying signing secret: %v", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	s, err := slack.SlashCommandParse(r)
	if err != nil {
		log.Printf("Error parsing slash command: %v", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	switch s.Command {
	case "/poll":
		modalRequest := generateModalRequest()
		_, err = c.Client.OpenView(s.TriggerID, modalRequest)
		if err != nil {
			log.Printf("Error opening view: %v", err)
		}
	default:
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func generateModalRequest() slack.ModalViewRequest {
	question := makeTextInputBlock("Name of Post", "It's time to poll!", "question", "question")
	choice1 := makeTextInputBlock("Choice 1", "2 hours", "choice1", "choice1")
	choice2 := makeTextInputBlock("Choice 2", "3 days", "choice2", "choice2")
	choice3 := makeTextInputBlock("Choice 3", "4 months", "choice3", "choice3")
	choice4 := makeTextInputBlock("Choice 4", "5 years", "choice4", "choice4")
	channelSelect := slack.NewOptionsSelectBlockElement(slack.OptTypeConversations, slack.NewTextBlockObject("plain_text", "channel to post in", false, false), "channelActionID")
	channelSelect.InitialConversation = "" // This should be the main channel ID
	channel := slack.NewInputBlock("channel", slack.NewTextBlockObject("plain_text", "channel to post in", false, false), channelSelect)

	// Create a ModalViewRequest with a header and two inputs
	titleText := slack.NewTextBlockObject("plain_text", "Fun fun poll time!", false, false)
	closeText := slack.NewTextBlockObject("plain_text", "nvm", false, false)
	submitText := slack.NewTextBlockObject("plain_text", "Party time!", false, false)

	blocks := slack.Blocks{
		BlockSet: []slack.Block{
			question,
			choice1,
			choice2,
			choice3,
			choice4,
			channel,
		},
	}

	var modalRequest slack.ModalViewRequest
	modalRequest.Type = slack.ViewType("modal")
	modalRequest.Title = titleText
	modalRequest.Close = closeText
	modalRequest.Submit = submitText
	modalRequest.Blocks = blocks
	return modalRequest
}

func makeTextInputBlock(title, placeholder, returnName, blockName string) *slack.InputBlock {
	text := slack.NewTextBlockObject("plain_text", title, false, false)
	placeholderStr := slack.NewTextBlockObject("plain_text", placeholder, false, false)
	textInputBlock := slack.NewPlainTextInputBlockElement(placeholderStr, returnName)
	return slack.NewInputBlock(blockName, text, textInputBlock)
}

// ModalHandler handles all action responses from slack
func (c *Config) ModalHandler(w http.ResponseWriter, r *http.Request) {
	err := c.verifySigningSecret(r)
	if err != nil {
		log.Printf("Error from verifySigningSecret: %v", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var i slack.InteractionCallback
	err = json.Unmarshal([]byte(r.FormValue("payload")), &i)
	if err != nil {
		log.Printf("JSON Unmarshal error: %v", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	switch i.Type {
	case slack.InteractionTypeBlockActions:
		blockSetIndex := 2
		messageTimestamp := i.Message.Timestamp
		userToAdd := "<@" + i.User.ID + ">"
		channel := i.Channel.ID
		optionSelected, _ := strconv.Atoi(i.ActionCallback.BlockActions[0].Value)
		newMessageBlocks := i.Message.Msg.Blocks

		groupTexts := []string{}
		for _, i := range []int{1, 3, 5, 7} {
			groupTexts = append(groupTexts, newMessageBlocks.BlockSet[blockSetIndex].(*slack.SectionBlock).Fields[i].Text)
		}
		groupTexts = updateGroups(userToAdd, optionSelected-1, groupTexts)
		for k, v := range map[int]int{1: 0, 3: 1, 5: 2, 7: 3} {
			newMessageBlocks.BlockSet[blockSetIndex].(*slack.SectionBlock).Fields[k].Text = groupTexts[v]
		}

		if err := c.updateMessage(channel, messageTimestamp, slack.MsgOptionBlocks(newMessageBlocks.BlockSet...)); err != nil {
			log.Printf("API update message error: %v", err)
			w.WriteHeader(http.StatusUnauthorized)
		}
		return
	case slack.InteractionTypeViewSubmission:
		buttons := []*slack.ButtonBlockElement{}
		textBlocks := []*slack.TextBlockObject{}
		for _, numStr := range []string{"1", "2", "3", "4"} {
			str := i.View.State.Values["choice"+numStr]["choice"+numStr].Value
			text := slack.NewTextBlockObject("plain_text", str, false, false)
			textBlocks = append(textBlocks, text, slack.NewTextBlockObject("mrkdwn", ":", false, false)) // turns out mrkdwn is the key
			button := slack.NewButtonBlockElement("actionID"+numStr, numStr, text)
			buttons = append(buttons, button)
		}
		actionBlock := slack.NewActionBlock("", buttons[0], buttons[1], buttons[2], buttons[3])
		sectionBlock := slack.SectionBlock{
			Type:   slack.MBTSection,
			Fields: textBlocks,
		}
		question := i.View.State.Values["question"]["question"].Value
		headerText := slack.NewTextBlockObject("plain_text", question, true, false)
		headerBlock := slack.SectionBlock{
			Type: slack.MBTSection,
			Text: headerText,
		}
		channel := i.View.State.Values["channel"]["channelActionID"].SelectedConversation
		if channel == "" {
			channel = i.User.ID
		}

		if err := c.sendMessage(channel, slack.MsgOptionBlocks(headerBlock, actionBlock, sectionBlock)); err != nil {
			log.Printf("API post message error: %v", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	default:
	}
}

func (c *Config) sendMessage(channel string, opts ...slack.MsgOption) error {
	_, _, err := c.Client.PostMessage(channel, opts...)
	return err
}

func (c *Config) updateMessage(channel, ts string, opts ...slack.MsgOption) error {
	_, _, _, err := c.Client.UpdateMessage(channel, ts, opts...)
	return err
}

func (c *Config) verifySigningSecret(r *http.Request) error {
	verifier, err := slack.NewSecretsVerifier(r.Header, c.SigningSecret)
	if err != nil {
		return err
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	// Need to use r.Body again when unmarshalling SlashCommand and InteractionCallback
	r.Body = io.NopCloser(bytes.NewBuffer(body))

	verifier.Write(body)
	return verifier.Ensure()
}

func appendUser(text, userID string) string {
	if text == ":" {
		return userID
	}
	return text + ", " + userID
}

func updateGroups(user string, index int, groups []string) []string {
	parsedGroups := make([][]string, 4)
	userIndex := -1
	for i := range groups {
		parsedGroups[i] = strings.Split(groups[i], ", ")
		if contains(user, parsedGroups[i]) {
			userIndex = i
		}
	}

	// If the user isn't anywhere, add them to the index
	if userIndex == -1 {
		groups[index] = appendUser(groups[index], user)
		return groups
	}

	// if the user is in the same group, remove them from the group
	parsedGroups[userIndex] = remove(user, parsedGroups[userIndex])
	for i := range parsedGroups {
		groups[i] = strings.Join(parsedGroups[i], ", ")
		if groups[i] == "" {
			groups[i] = ":" // always have some text
		}
	}

	// if the user is in a different group, remove them from that group and add them to the new one
	if index != userIndex {
		groups[index] = appendUser(groups[index], user)
	}
	return groups
}

func contains(text string, arr []string) bool {
	for _, s := range arr {
		if text == s {
			return true
		}
	}
	return false
}

func remove(text string, arr []string) []string {
	var index int
	for i := range arr {
		if text == arr[i] {
			index = i
			break
		}
	}
	return append(arr[:index], arr[index+1:]...)
}
