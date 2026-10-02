package daemon

import (
	"math/rand/v2"
	"strconv"
	"strings"
)

var adjectives = strings.Fields(`
able agile amber ample azure balmy beamy bold brave breezy bright brisk
bubbly calm candid cheery chipper civil clever cozy crisp curly daring dapper
deft eager early easy fair fancy fast fluffy fond frank free fresh frisky
gentle giddy glad golden grand happy hardy hearty humble jolly jovial keen
kind lively loyal lucky mellow merry mighty mild minty modest neat nimble
noble perky plucky polite proud quick quiet rapid ready regal rosy rustic
sandy savvy shiny silky silver snappy snug sound spry steady sturdy sunny
super swift tidy trusty upbeat vivid warm wise witty zesty zippy
`)

var nouns = strings.Fields(`
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

// freshName is an adjective-noun name no session has. Callers hold d.mu.
func (d *Daemon) freshName() string {
	for n := 0; ; n++ {
		name := adjectives[rand.IntN(len(adjectives))] + "-" + nouns[rand.IntN(len(nouns))]
		if n >= 100 { // ten thousand pairs nearly all taken
			name += "-" + strconv.Itoa(n)
		}
		if !d.nameTaken(name, "") {
			return name
		}
	}
}
