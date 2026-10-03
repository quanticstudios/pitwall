package model

import (
	"slices"
	"strconv"
	"strings"
)

var nameAdjectives = strings.Fields(`
able agile amber ample azure balmy beamy bold brave breezy bright brisk
bubbly calm candid cheery chipper civil clever cozy crisp curly daring dapper
deft eager early easy fair fancy fast fluffy fond frank free fresh frisky
gentle giddy glad golden grand happy hardy hearty humble jolly jovial keen
kind lively loyal lucky mellow merry mighty mild minty modest neat nimble
noble perky plucky polite proud quick quiet rapid ready regal rosy rustic
sandy savvy shiny silky silver snappy snug sound spry steady sturdy sunny
super swift tidy trusty upbeat vivid warm wise witty zesty zippy
`)

var nameNouns = strings.Fields(`
alpaca badger bat beaver bee bison bobcat camel cat cheetah chipmunk cobra
condor cougar coyote crab crane cricket deer dingo dolphin donkey dove duck
eagle egret elk emu falcon ferret finch flamingo fox frog gazelle gecko
gibbon giraffe goat goose gopher heron hippo horse ibex iguana impala jackal
jaguar kestrel kiwi koala lark lemur leopard lion llama lynx magpie manatee
marmot marten meerkat mink mole moose newt ocelot okapi orca osprey otter owl
panda panther parrot pelican penguin pigeon puffin puma quail rabbit raven
robin salmon seal shark sloth sparrow squid stork swan tapir tiger toucan
trout turtle walrus weasel whale wolf wombat wren yak zebra
`)

// SessionName is the adjective-noun session name picked by intn (such as
// rand.IntN), with "-<try>" appended from the 100th try on, when nearly
// every pair is taken.
func SessionName(intn func(int) int, try int) string {
	name := nameAdjectives[intn(len(nameAdjectives))] + "-" + nameNouns[intn(len(nameNouns))]
	if try >= 100 {
		name += "-" + strconv.Itoa(try)
	}
	return name
}

// IsSessionName reports whether name has the shape SessionName makes.
func IsSessionName(name string) bool {
	parts := strings.Split(name, "-")
	if len(parts) == 3 {
		if _, err := strconv.Atoi(parts[2]); err != nil {
			return false
		}
	} else if len(parts) != 2 {
		return false
	}
	return slices.Contains(nameAdjectives, parts[0]) && slices.Contains(nameNouns, parts[1])
}
