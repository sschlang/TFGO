package main

// setup.go: functions for game setup (and termination)

import (
	"time"
	"math"
	"math/rand"
	"net"
	"encoding/json"
	"strconv"
	"fmt"
	"sync"
)

// return the central position of a game
func (g *Game) findCenter() Location {
	X := 0.0
	Y := 0.0
	c := float64(len(g.Boundaries))
	for _, val := range g.Boundaries {
		X += val.P.X
		Y += val.P.Y
	}
	return Location{X / c, Y / c}
}

// register a new player, but do not add them to any team.
// performed whenever someone chooses Create Game or Join
// Game from the primary app menu
func createPlayer(conn net.Conn, name, icon string) *Player {
	var p Player
	p.Name = name
	p.Icon = icon

	p.Chan = make(chan map[string]interface{})
	if !isTesting {
		p.Encoder = json.NewEncoder(conn)
	}

	p.Status = NORMAL
	p.Health = MAXHEALTH()
	p.Armor = 0
	p.SelectedWeapon = SWORD
	p.Weapons = make(map[string]Weapon)
	p.Weapons["Sword"] = SWORD

	go p.sender()

	fmt.Printf("Player %s created.\n", p.Name)

	return &p
}

// the following random string generation code is heavily inspired by the
// example code at https://siongui.github.io/2015/04/13/go-generate-random-string/
var r = rand.New(rand.NewSource(time.Now().UnixNano()))
const idChars = "abcdefghijklmnopqrstuvwxyz1234567890"
var rLock sync.Mutex

// generate a random unique game ID
func createGameID() string {
	rLock.Lock()
	candidate := make([]byte, 16)
	for i := range candidate {
		candidate[i] = idChars[r.Intn(len(idChars))]
	}
	rLock.Unlock()

	if _, exists := games[string(candidate)]; exists {
		return createGameID()
	} else {
		return string(candidate)
	}
}

// determine game boundaries based on vertex information
func (g *Game) setBoundaries(boundaries []interface{}) {
	for _, val := range boundaries {
		vertex := val.(map[string]interface{})
		p := Location{X: degreeToMeter(vertex["X"].(float64)), Y: degreeToMeter(vertex["Y"].(float64))}
		g.Boundaries = append(g.Boundaries, Border{P: p})
	}

	for i, boundary := range g.Boundaries {
		var index int
		if i == 0 {
			index = len(g.Boundaries) - 1
		} else {
			index = i - 1
		}

		prev := g.Boundaries[index].P
		g.Boundaries[index].D = Direction{boundary.P.X - prev.X, boundary.P.Y - prev.Y}
	}
}

// register a new game instance, with the host as its first player
func createGame(conn net.Conn, data map[string]interface{}) (*Game, *Player) {

	var g Game
	g.ID = createGameID()
	g.Name = data["Name"].(string)
	g.Password = data["Password"].(string)
	g.Description = data["Description"].(string)
	g.PlayerLimit = int(data["PlayerLimit"].(float64))
	g.PointLimit = int(data["PointLimit"].(float64))
	g.TimeLimit, _ = time.ParseDuration(data["TimeLimit"].(string))
	g.Status = CREATING
	g.Mode = stringToMode[data["Mode"].(string)]
	g.setBoundaries(data["Boundaries"].([]interface{}))

	host := data["Host"].(map[string]interface{})
	p := createPlayer(conn, host["Name"].(string), host["Icon"].(string))

	g.RedTeam = &Team{Name: "Red"}
	g.BlueTeam = &Team{Name: "Blue"}
	g.Players = map[string]*Player{p.Name : p}

	games[g.ID] = &g

	fmt.Printf("Game %s with ID %s created.\n", g.Name, g.ID)
	fmt.Printf("Player %s added to game %s.\n", p.Name, g.ID)

	return &g, p
}

// add a player to a game if possible
func (p *Player) joinGame(gameID string) *Game {
	target := games[gameID]
	if len(target.Players) == target.PlayerLimit {
		sendJoinGameError(p, "GameFull")
		return nil
	} else if target.Status != CREATING {
		sendJoinGameError(p, "GameStarted")
		return nil
	} else {
		target.Players[p.Name] = p
		sendPlayerListUpdate(target)

		fmt.Printf("Player %s added to game %s.\n", p.Name, gameID)

		return target
	}
}

// determines whether a CP or pickup with the given location and radius
// intersects with any existing CPs or pickups
func noIntersections(g *Game, loc Location, r float64) bool {
	for _, val := range g.ControlPoints {
		if distance(loc, val.Location) <= (r + val.Radius) {
			return false
		}
	}
	for _, val := range g.Pickups {
		if distance(loc, val.Location) <= (r + PICKUPRADIUS()) {
			return false
		}
	}
	return true
}

// determine locations and radii of bases and control points
func (g *Game) generateObjectives(numCP int) {
	minX := math.MaxFloat64
	maxX := -math.MaxFloat64
	minY := math.MaxFloat64
	maxY := -math.MaxFloat64
	for _, val := range g.Boundaries {
		if val.P.X < minX {
			minX = val.P.X
		}
		if val.P.X > maxX {
			maxX = val.P.X
		}
		if val.P.Y < minY {
			minY = val.P.Y
		}
		if val.P.Y > maxY {
			maxY = val.P.Y
		}
	}
	xrange := maxX - minX
	yrange := maxY - minY

	// set up base locations for the two teams
	baseRadius := BASERADIUS(xrange, yrange)
	g.RedTeam.BaseRadius = baseRadius
	g.BlueTeam.BaseRadius = baseRadius
	xoffset := baseRadius + 2
	yoffset := baseRadius + 2
	if xrange < 20 {
		xoffset = (xrange - baseRadius * 2) / 4 + baseRadius
	}
	if yrange < 20 {
		yoffset = (yrange - baseRadius * 2) / 4 + baseRadius
	}
	if xrange > yrange {
		mid := yrange / 2
		g.RedTeam.Base = Location{maxX - xoffset, mid}
		g.BlueTeam.Base = Location{minX + xoffset, mid}
	} else {
		mid := xrange / 2
		g.RedTeam.Base = Location{mid, maxY - yoffset}
		g.BlueTeam.Base = Location{mid, minY + yoffset}
	}

	// set up control points
	cpRadius := CPRADIUS()
	if g.Mode == MULTICAP {
		// make sure that control points don't intersect bases
		minX = minX + 2 * xoffset + cpRadius
		maxX = maxX - 2 * xoffset - cpRadius
		minY = minY + 2 * yoffset + cpRadius
		maxY = maxY - 2 * yoffset - cpRadius
		xrange = maxX - minX
		yrange = maxY - minY

		// generate control points
		g.ControlPoints = make(map[string]*ControlPoint)
		rLock.Lock()
		for i := 0; i < numCP; i++ {
			cpLoc := Location{minX + r.Float64() * xrange, minY + r.Float64() * yrange}
			if inGameBounds(g, cpLoc) && noIntersections(g, cpLoc, cpRadius) {
				id := "CP" + strconv.Itoa(i+1)
				cp := &ControlPoint{ID: id, Location: cpLoc, Radius: cpRadius}
				g.ControlPoints[id] = cp
			} else {
				i-- // if this location is invalid, decrement i so that it doesn't count towards numCP
			}
		}
		rLock.Unlock()
	} else if g.Mode == SINGLECAP {
		g.ControlPoints["CP1"] = &ControlPoint{ID: "CP1", Location: g.findCenter(), Radius: cpRadius}
	} else {
		cpLoc := Location{(g.RedTeam.Base.X + g.BlueTeam.Base.X) / 2, (g.RedTeam.Base.Y + g.BlueTeam.Base.Y) / 2}
		g.ControlPoints["CP1"] = &ControlPoint{ID: "CP1", Location: cpLoc, Radius: cpRadius}
	}

	// generate pickups
	xSpread := (int)(math.Floor(xrange / PICKUPDISTRIBUTION()))
	ySpread := (int)(math.Floor(yrange / PICKUPDISTRIBUTION()))
	half_range := math.Min(xrange, yrange)/2
	for i := 0; i < xSpread; i ++ {
		for j := 0; j < ySpread; j ++ {
			generatePickup(g, (float64)(i) * PICKUPDISTRIBUTION(), (float64)(j) * PICKUPDISTRIBUTION(), half_range)
		}
	}
}

// semi-randomly places a pickup in the game
func generatePickup(g *Game, minX, minY, half_range float64) {
	rLock.Lock()
	xoff := r.Float64() * PICKUPDISTRIBUTION()
	yoff := r.Float64() * PICKUPDISTRIBUTION()
	rLock.Unlock()
	loc := Location{minX + xoff, minY + yoff}
	if !(inGameBounds(g, loc) && noIntersections(g, loc, PICKUPRADIUS())) {
		rLock.Lock()
		xoff = r.Float64() * PICKUPDISTRIBUTION()
		yoff = r.Float64() * PICKUPDISTRIBUTION()
		rLock.Unlock()
		loc = Location{minX + xoff, minY + yoff}
		if !(inGameBounds(g, loc) && noIntersections(g, loc, PICKUPRADIUS())) {
			return
		}
	}
	dist := distance(g.findCenter(), loc)
	healthprob := 50 * dist/half_range
	armorprob := 50 - 25 * dist/half_range
	weaponprob := 50 - 10 * dist/half_range
	totalprob := healthprob + armorprob + weaponprob
	healthprob = healthprob / totalprob
	armorprob = armorprob / totalprob + healthprob
	rLock.Lock()
	choice := r.Float64()
	rLock.Unlock()
	var newPickup Pickup
	if choice < healthprob {
		newPickup = makeHealthPickup(chooseArmorHealth(g, loc, half_range * 2))
	} else if choice < armorprob {
		newPickup = makeArmorPickup(chooseArmorHealth(g, loc, half_range * 2))
	} else {
		rLock.Lock()
		wn := r.Intn(len(weaponsSlice))
		rLock.Unlock()
		newPickup = makeWeaponPickup(weaponsSlice[wn])
	}
	newPickupSpot := PickupSpot{
		Location: loc,
		Pickup: newPickup,
		Available: true,
	}
	g.Pickups = append(g.Pickups, newPickupSpot)
	return
}

// assign players to teams at the start of a game
func (g *Game) randomizeTeams() {
	teamSize := len(g.Players) / 2
	count := 0

	// iteration order through maps is random
	for _, player := range g.Players {
		if count < teamSize {
			player.Team = g.RedTeam
		} else {
			player.Team = g.BlueTeam
		}
		count++
	}
}

// begin a game, determining objective and team information and
// starting goroutines that will run for the duration of the game
func (g *Game) start() {
	// for now we're doing just one ControlPoint, that may change later
	g.generateObjectives(1)
	g.randomizeTeams()

	startTime := time.Now().Add(time.Second * 5)
	sendGameStartInfo(g, startTime)
	go g.awaitStart(startTime)
}

func (g *Game) awaitStart(startTime time.Time) {
	time.Sleep(time.Until(startTime))
	g.Status = PLAYING
	g.Timer = time.AfterFunc(g.TimeLimit, func() {
		g.stop()
	})
	go sendGameUpdates(g)
	for _, cp := range g.ControlPoints {
		go cp.updateTicker(g)
	}
}

// end a game, signalling and performing resource cleanup
func (g *Game) stop() {
	sendGameOver(g)
	g.Status = GAMEOVER
	delete(games, g.ID)
	for _, player := range g.Players {
		close(player.Chan)
	}
}
