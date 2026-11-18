package config

import "fmt"

// Chat is [chat]: how a chat tab's transcript draws.
type Chat struct {
	Width string `toml:"width" enum:"comfortable,standard,full" doc:"The transcript's column: comfortable is 736 dp, standard 960 dp (fits a 120-column diff), full uses the whole tab. Reply prose stops near 96 characters either way"`
}

// Chat column widths in dp.
const (
	ChatComfortable = 736
	ChatStandard    = 960
)

// ChatSettings is [chat] resolved.
type ChatSettings struct {
	Width int // the column's widest, in dp; 0 is the whole tab
}

func defaultChat() Chat { return Chat{Width: "standard"} }

// resolveChat fills in [chat]'s values from c.
func resolveChat(c Chat) (ChatSettings, []issue) {
	switch c.Width {
	case "", "standard":
		return ChatSettings{Width: ChatStandard}, nil
	case "comfortable":
		return ChatSettings{Width: ChatComfortable}, nil
	case "full":
		return ChatSettings{}, nil
	}
	return ChatSettings{Width: ChatStandard}, []issue{{"chat.width", fmt.Sprintf("%q is not comfortable, standard or full; using standard", c.Width)}}
}
