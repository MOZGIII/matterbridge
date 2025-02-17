package bdiscord

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/bwmarrin/discordgo"
)

func (b *Bdiscord) getNick(user *discordgo.User) string {
	b.membersMutex.RLock()
	defer b.membersMutex.RUnlock()

	if member, ok := b.userMemberMap[user.ID]; ok {
		if member.Nick != "" {
			// Only return if nick is set.
			return member.Nick
		}
		// Otherwise return username.
		return user.Username
	}

	// If we didn't find nick, search for it.
	member, err := b.c.GuildMember(b.guildID, user.ID)
	if err != nil {
		b.Log.Warnf("Failed to fetch information for member %#v: %s", user, err)
		return user.Username
	} else if member == nil {
		b.Log.Warnf("Got no information for member %#v", user)
		return user.Username
	}
	b.userMemberMap[user.ID] = member
	b.nickMemberMap[member.User.Username] = member
	if member.Nick != "" {
		b.nickMemberMap[member.Nick] = member
		return member.Nick
	}
	return user.Username
}

func (b *Bdiscord) getGuildMemberByNick(nick string) (*discordgo.Member, error) {
	b.membersMutex.RLock()
	defer b.membersMutex.RUnlock()

	if member, ok := b.nickMemberMap[nick]; ok {
		return member, nil
	}
	return nil, errors.New("Couldn't find guild member with nick " + nick) // This will most likely get ignored by the caller
}

func (b *Bdiscord) getChannelID(name string) string {
	if strings.Contains(name, "/") {
		return b.getCategoryChannelID(name)
	}
	b.channelsMutex.RLock()
	defer b.channelsMutex.RUnlock()

	idcheck := strings.Split(name, "ID:")
	if len(idcheck) > 1 {
		return idcheck[1]
	}
	for _, channel := range b.channels {
		if channel.Name == name && channel.Type == discordgo.ChannelTypeGuildText {
			return channel.ID
		}
	}
	return ""
}

func (b *Bdiscord) getCategoryChannelID(name string) string {
	b.channelsMutex.RLock()
	defer b.channelsMutex.RUnlock()
	res := strings.Split(name, "/")
	// shouldn't happen because function should be only called from getChannelID
	if len(res) != 2 {
		return ""
	}
	catName, chanName := res[0], res[1]
	for _, channel := range b.channels {
		// if we have a parentID, lookup the name of that parent (category)
		// and if it matches return it
		if channel.Name == chanName && channel.ParentID != "" {
			for _, cat := range b.channels {
				if cat.ID == channel.ParentID && cat.Name == catName {
					return channel.ID
				}
			}
		}
	}
	return ""
}

func (b *Bdiscord) getChannelName(id string) string {
	b.channelsMutex.RLock()
	defer b.channelsMutex.RUnlock()

	for _, channel := range b.channels {
		if channel.ID == id {
			return b.getCategoryChannelName(channel.Name, channel.ParentID)
		}
	}
	return ""
}

func (b *Bdiscord) getCategoryChannelName(name, parentID string) string {
	var usesCat bool
	// do we have a category configuration in the channel config
	for _, c := range b.channelInfoMap {
		if strings.Contains(c.Name, "/") {
			usesCat = true
			break
		}
	}
	// configuration without category, return the normal channel name
	if !usesCat {
		return name
	}
	// create a category/channel response
	for _, c := range b.channels {
		if c.ID == parentID {
			name = c.Name + "/" + name
		}
	}
	return name
}

var (
	// See https://discordapp.com/developers/docs/reference#message-formatting.
	channelMentionRE = regexp.MustCompile("<#[0-9]+>")
	emojiRE          = regexp.MustCompile("<(:.*?:)[0-9]+>")
	userMentionRE    = regexp.MustCompile("@[^@\n]{1,32}")
)

func (b *Bdiscord) replaceChannelMentions(text string) string {
	replaceChannelMentionFunc := func(match string) string {
		channelID := match[2 : len(match)-1]
		channelName := b.getChannelName(channelID)

		// If we don't have the channel refresh our list.
		if channelName == "" {
			var err error
			b.channels, err = b.c.GuildChannels(b.guildID)
			if err != nil {
				return "#unknownchannel"
			}
			channelName = b.getChannelName(channelID)
		}
		return "#" + channelName
	}
	return channelMentionRE.ReplaceAllStringFunc(text, replaceChannelMentionFunc)
}

func (b *Bdiscord) replaceUserMentions(text string) string {
	replaceUserMentionFunc := func(match string) string {
		var (
			err      error
			member   *discordgo.Member
			username string
		)

		usernames := enumerateUsernames(match[1:])
		for _, username = range usernames {
			b.Log.Debugf("Testing mention: '%s'", username)
			member, err = b.getGuildMemberByNick(username)
			if err == nil {
				break
			}
		}
		if member == nil {
			return match
		}
		return strings.Replace(match, "@"+username, member.User.Mention(), 1)
	}
	return userMentionRE.ReplaceAllStringFunc(text, replaceUserMentionFunc)
}

func (b *Bdiscord) stripCustomoji(text string) string {
	return emojiRE.ReplaceAllString(text, `$1`)
}

func (b *Bdiscord) replaceAction(text string) (string, bool) {
	if strings.HasPrefix(text, "_") && strings.HasSuffix(text, "_") {
		return text[1 : len(text)-1], true
	}
	return text, false
}

// splitURL splits a webhookURL and returns the ID and token.
func (b *Bdiscord) splitURL(url string) (string, string) {
	const (
		expectedWebhookSplitCount = 7
		webhookIdxID              = 5
		webhookIdxToken           = 6
	)
	webhookURLSplit := strings.Split(url, "/")
	if len(webhookURLSplit) != expectedWebhookSplitCount {
		b.Log.Fatalf("%s is no correct discord WebhookURL", url)
	}
	return webhookURLSplit[webhookIdxID], webhookURLSplit[webhookIdxToken]
}

func enumerateUsernames(s string) []string {
	onlySpace := true
	for _, r := range s {
		if !unicode.IsSpace(r) {
			onlySpace = false
			break
		}
	}
	if onlySpace {
		return nil
	}

	var username, endSpace string
	var usernames []string
	skippingSpace := true
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !skippingSpace {
				usernames = append(usernames, username)
				skippingSpace = true
			}
			endSpace += string(r)
			username += string(r)
		} else {
			endSpace = ""
			username += string(r)
			skippingSpace = false
		}
	}
	if endSpace == "" {
		usernames = append(usernames, username)
	}
	return usernames
}

// webhookExecute executes a webhook.
// webhookID: The ID of a webhook.
// token    : The auth token for the webhook
// wait	    : Waits for server confirmation of message send and ensures that the return struct is populated (it is nil otherwise)
func (b *Bdiscord) webhookExecute(webhookID, token string, wait bool, data *discordgo.WebhookParams) (st *discordgo.Message, err error) {
	uri := discordgo.EndpointWebhookToken(webhookID, token)

	if wait {
		uri += "?wait=true"
	}
	response, err := b.c.RequestWithBucketID("POST", uri, data, discordgo.EndpointWebhookToken("", ""))
	if !wait || err != nil {
		return nil, err
	}

	err = json.Unmarshal(response, &st)
	if err != nil {
		return nil, discordgo.ErrJSONUnmarshal
	}

	return st, nil
}
