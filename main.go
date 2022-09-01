package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slack-go/slack"
	"github.com/urfave/cli/v2"
)

func check(e error) {
	if e != nil {
		panic(e)
	}
}

func main() {
	app := cli.NewApp()
	app.Name = "slack-dump"
	app.Usage = "export channel and group history to the Slack export format include Direct message"
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    "token",
			Aliases: []string{"t"},
			Value:   "",
			Usage:   "a Slack API token: (see: https://api.slack.com/web)",
			EnvVars: []string{"SLACK_API_TOKEN"},
		},
		&cli.StringFlag{
			Name:    "output",
			Aliases: []string{"o"},
			Value:   "",
			Usage:   "Output directory path. Default: current directory path",
			EnvVars: []string{""},
		},
		&cli.BoolFlag{
			Name:    "mattermost",
			Aliases: []string{"m"},
			Value:   false,
			Usage:   "Enables Mattermost format",
		},
	}
	app.Authors = []*cli.Author{
		{
			Name:  "Joe Fitzgerald",
			Email: "jfitzgerald@pivotal.io",
		},
		{
			Name:  "Sunyong Lim",
			Email: "dicebattle@gmail.com",
		},
		{
			Name:  "Yoshihiro Misawa",
			Email: "myoshi321go@gmail.com",
		},
		{
			Name:  "takameron",
			Email: "tech@takameron.info",
		},
		{
			Name:  "Toru Nakashika",
			Email: "nakashika@uec.ac.jp",
		},
	}
	app.Version = "1.4.0"
	app.Action = func(c *cli.Context) error {
		token := c.String("token")
		if token == "" {
			fmt.Println("ERROR: the token flag is required...")
			fmt.Println("")
			cli.ShowAppHelp(c)
			os.Exit(2)
		}

		outputDir := c.String("output")
		if outputDir == "" {
			pwd, err := os.Getwd()
			check(err)
			outputDir = pwd
		}

		matterFlag := c.Bool("mattermost")

		// create directory if outputDir does not exists
		if _, err := os.Stat(outputDir); os.IsNotExist(err) {
			os.MkdirAll(outputDir, 0755)
		}

		rooms := c.Args().Slice()
		api := slack.New(token)
		res, err := api.AuthTest()
		if err != nil {
			fmt.Println("ERROR: the token you used is not valid...")
			os.Exit(2)
		}

		// Create working directory
		dir, err := ioutil.TempDir("", "slack-dump")
		check(err)

		dump(api, res, dir, rooms, matterFlag)
		archive(dir, outputDir)

		return nil
	}

	app.Run(os.Args)
}

func archive(inFilePath, outputDir string) {
	ts := time.Now().Format("20060102150405")
	outZipPath := path.Join(outputDir, fmt.Sprintf("slackdump-%s.zip", ts))

	outZip, err := os.Create(outZipPath)
	check(err)
	defer outZip.Close()

	zipWriter := zip.NewWriter(outZip)
	defer zipWriter.Close()

	// Set compression level: flate.BestCompression
	zipWriter.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})

	basePath := filepath.Dir(inFilePath)

	err = filepath.Walk(inFilePath, func(filePath string, fileInfo os.FileInfo, err error) error {
		if err != nil || fileInfo.IsDir() {
			return err
		}

		relativeFilePath, err := filepath.Rel(basePath, filePath)
		if err != nil {
			return err
		}

		// do not include ioutil.TempDir name
		relativeFilePathArr := strings.Split(relativeFilePath, string(filepath.Separator))
		relativeFilePath = path.Join(relativeFilePathArr[1:]...)

		archivePath := path.Join(filepath.SplitList(relativeFilePath)...)

		//Display the output file name
		// fmt.Println(archivePath)

		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer file.Close()

		zipFileWriter, err := zipWriter.Create(archivePath)
		if err != nil {
			return err
		}

		_, err = io.Copy(zipFileWriter, file)
		return err
	})

	check(err)
}

// MarshalIndent is like json.MarshalIndent but applies Slack's weird JSON
// escaping rules to the output.
func MarshalIndent(v interface{}, prefix string, indent string) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		return nil, err
	}

	b = bytes.Replace(b, []byte("\\u003c"), []byte("<"), -1)
	b = bytes.Replace(b, []byte("\\u003e"), []byte(">"), -1)
	b = bytes.Replace(b, []byte("\\u0026"), []byte("&"), -1)
	b = bytes.Replace(b, []byte("/"), []byte("\\/"), -1)

	return b, nil
}

func dump(api *slack.Client, res *slack.AuthTestResponse, dir string, rooms []string, matterFlag bool) {
	channels := fetchChannel(api)
	users, err := api.GetUsers()
	check(err)
	var public_channels, direct_channels, private_channels, group_channels []slack.Channel
	r_mpdm := regexp.MustCompile(`^mpdm-(.+)-\d$`)

	for _, c := range channels {
		kind := ""
		name := ""
		members := []string{}
		if c.IsIM {
			kind = "direct_message"
			for _, usr := range users {
				if c.User == usr.ID {
					name = usr.Name
					members = []string{res.UserID, usr.ID}
				}
			}
			c.Name = name
			c.Members = members
			direct_channels = append(direct_channels, c)
		} else if c.IsMpIM {
			kind = "direct_message"
			name = c.Name
			members = strings.Split(r_mpdm.ReplaceAllString(c.Name, "$1"), "--")
			for i, member := range members {
				for _, usr := range users {
					if member == usr.Name {
						members[i] = usr.ID
					}
				}
			}
			c.Members = members
			group_channels = append(group_channels, c)
		} else if c.IsChannel && !c.IsGroup && !c.IsPrivate {
			kind = "channel"
			name = c.Name
			public_channels = append(public_channels, c)
		} else if (!c.IsChannel && !c.IsGroup) || (c.IsChannel && c.IsPrivate) {
			kind = "private_channel"
			name = c.Name
			private_channels = append(private_channels, c)
		}

		ok := len(rooms) == 0 || (len(rooms) > 0 && hasArrayItem(rooms, name))

		if kind != "" && ok {
			fmt.Println(name)
			dumpChannel(api, c.ID, name, kind, dir, matterFlag)
		}
	}

	dumpJson(public_channels, "channels.json", dir)
	dumpJson(direct_channels, "dms.json", dir)
	dumpJson(private_channels, "groups.json", dir)
	dumpJson(group_channels, "mpims.json", dir)
	dumpJson(users, "users.json", dir)
}

func dumpJson(v interface{}, name string, dir string) {
	v_, err := MarshalIndent(v, "", "    ")
	check(err)
	err = ioutil.WriteFile(path.Join(dir, name), v_, 0644)
	check(err)
}

func fetchChannel(api *slack.Client) []slack.Channel {
	channelParams := slack.GetConversationsParameters{}
	channelParams.Types = []string{"public_channel", "private_channel", "mpim", "im"}
	channelParams.Limit = 1000

	// Fetch Channel
	chs, nextCursor, err := api.GetConversations(&channelParams)
	check(err)
	channels := chs
	if len(channels) > 0 {
		for {
			if nextCursor == "" {
				break
			}

			channelParams.Cursor = nextCursor
			chs, nextCursor, err := api.GetConversations(&channelParams)
			check(err)
			length := len(chs)
			if length > 0 {
				channelParams.Cursor = nextCursor
				channels = append(channels, chs...)
			}
		}
	}

	return channels
}

func dumpChannel(api *slack.Client, id string, name string, kind string, dir string, matterFlag bool) {
	messages := fetchHistory(api, id)
	channelPath := ""

	if len(messages) == 0 {
		return
	}

	currentFilename := ""
	if matterFlag {
		channelPath = name
	} else {
		channelPath = path.Join(kind, name)
	}
	var currentMessages []slack.Message
	for _, message := range messages {
		ts := parseTimestamp(message.Timestamp)
		filename := fmt.Sprintf("%d-%02d-%02d.json", ts.Year(), ts.Month(), ts.Day())
		if (message.Files != nil) && matterFlag {
			message.Upload = true
		}

		if currentFilename != filename {
			writeMessagesFile(currentMessages, dir, channelPath, currentFilename)
			currentMessages = make([]slack.Message, 0, 5)
			currentFilename = filename
		}

		currentMessages = append(currentMessages, message)
	}
	writeMessagesFile(currentMessages, dir, channelPath, currentFilename)
}

func fetchHistory(api *slack.Client, ID string) []slack.Message {
	historyParams := slack.GetConversationHistoryParameters{}
	historyParams.ChannelID = ID
	historyParams.Limit = 1000
	replyParams := slack.GetConversationRepliesParameters{}
	replyParams.ChannelID = ID

	// Fetch History
	history, err := api.GetConversationHistory(&historyParams)
	check(err)
	messages := history.Messages
	if len(messages) > 0 {
		for {
			replies := []slack.Reply{}
			for i, message := range messages {
				if message.ReplyCount > 0 {
					replyParams.Timestamp = message.ThreadTimestamp
					replyMessages, _, _, err := api.GetConversationReplies(&replyParams)
					check(err)
					replies = make([]slack.Reply, message.ReplyCount)
					for j, reply := range replies {
						reply.User = replyMessages[j+1].User
						reply.Timestamp = replyMessages[j+1].Timestamp
						replies[j] = reply
					}
					messages[i].Replies = replies
					messages = append(messages, replyMessages[1:]...)
				}
			}
			if !history.HasMore {
				break
			}

			historyParams.Cursor = history.ResponseMetaData.NextCursor
			history, err = api.GetConversationHistory(&historyParams)
			check(err)
			length := len(history.Messages)
			if length > 0 {
				historyParams.Cursor = history.ResponseMetaData.NextCursor
				messages = append(messages, history.Messages...)
			}
		}
	}

	sortMessages(messages)
	return messages
}

func parseTimestamp(timestamp string) *time.Time {
	if utf8.RuneCountInString(timestamp) <= 0 {
		return nil
	}

	ts := timestamp

	if strings.Contains(timestamp, ".") {
		e := strings.Split(timestamp, ".")
		if len(e) != 2 {
			return nil
		}
		ts = e[0]
	}

	i, err := strconv.ParseInt(ts, 10, 64)
	check(err)
	tm := time.Unix(i, 0).Local()
	return &tm
}

func writeMessagesFile(messages []slack.Message, dir string, channelPath string, filename string) {
	if len(messages) == 0 || dir == "" || channelPath == "" || filename == "" {
		return
	}
	channelDir := path.Join(dir, channelPath)
	err := os.MkdirAll(channelDir, 0755)
	check(err)

	data, err := MarshalIndent(messages, "", "    ")
	check(err)
	err = ioutil.WriteFile(path.Join(channelDir, filename), data, 0644)
	check(err)
}

func hasArrayItem(arr []string, str string) bool {
	for _, v := range arr {
		if v == str {
			return true
		}
	}
	return false
}

func sortMessages(messages []slack.Message) []slack.Message {
	N := len(messages)
	for i := 0; i < N-1; i++ {
		for j := N - 1; j > i; j-- {
			if messages[j-1].Timestamp > messages[j].Timestamp {
				tmp := messages[j-1]
				messages[j-1] = messages[j]
				messages[j] = tmp
			}
		}
	}
	return messages
}
