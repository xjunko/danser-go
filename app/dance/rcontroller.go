package dance

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/karrick/godirwalk"
	"github.com/wieku/danser-go/app/dance/input"
	"github.com/wieku/danser-go/app/dance/movers"
	"github.com/wieku/danser-go/app/dance/schedulers"
	"github.com/wieku/danser-go/app/dance/spinners"
	"github.com/wieku/danser-go/framework/env"
	"github.com/wieku/danser-go/framework/math/mutils"
	"github.com/wieku/rplpa"

	//"github.com/thehowl/go-osuapi"
	"github.com/wieku/danser-go/app/beatmap"
	"github.com/wieku/danser-go/app/beatmap/difficulty"
	"github.com/wieku/danser-go/app/graphics"
	"github.com/wieku/danser-go/app/rulesets/osu"
	"github.com/wieku/danser-go/app/settings"

	//"github.com/wieku/danser-go/app/utils"
	"io/ioutil"
	"log"

	"github.com/wieku/danser-go/framework/math/math32"
	"github.com/wieku/danser-go/framework/math/vector"

	//"net/http"
	//"net/url"
	"os"
	"path/filepath"

	//"strconv"
	"strings"
	"unicode"
)

const replaysMaster = "replays"

type RpData struct {
	Name      string
	Mods      string
	ModsV     difficulty.Modifier
	Accuracy  float64
	Combo     int64
	MaxCombo  int64
	Grade     osu.Grade
	scoreID   int64
	ScoreTime time.Time
}

type subControl struct {
	danceController Controller
	replayIndex     int
	replayTime      int64
	frames          []*rplpa.ReplayData
	newHandling     bool
	lastTime        int64
	oldSpinners     bool
	relaxController *input.RelaxInputProcessor
	mouseController schedulers.Scheduler
	mods            difficulty.Modifier
}

func NewSubControl() *subControl {
	control := new(subControl)
	return control
}

type ReplayController struct {
	bMap        *beatmap.BeatMap
	replays     []RpData
	cursors     []*graphics.Cursor
	controllers []*subControl
	ruleset     *osu.OsuRuleSet
	lastTime    float64
}

func NewReplayController() Controller {
	_ = os.MkdirAll(filepath.Join(env.DataDir(), replaysMaster), 0755)

	return &ReplayController{lastTime: -200}
}

func (controller *ReplayController) SetBeatMap(beatMap *beatmap.BeatMap) {
	controller.bMap = beatMap

	organizeReplays()

	candidates := make([]*rplpa.Replay, 0)

	localReplay := false
	if settings.REPLAY != "" {
		log.Println("Loading: ", settings.REPLAY)

		data, err := ioutil.ReadFile(settings.REPLAY)
		if err != nil {
			panic(err)
		}

		replayD, _ := rplpa.ParseReplay(data)

		if replayD.ReplayData == nil || len(replayD.ReplayData) == 0 {
			log.Println("Excluding for missing input data:", replayD.Username)
		} else {
			candidates = append(candidates, replayD)

			localReplay = true
		}
	} else if settings.Knockout.MaxPlayers > 0 || (settings.KNOCKOUTREPLAYS != nil && len(settings.KNOCKOUTREPLAYS) > 0) { // ignore max player limit with new knockout
		candidates = controller.getCandidates()
	}

	if !localReplay {
		// NOTE [xJunko]: Dont sort based on score.
		// sort.Slice(candidates, func(i, j int) bool {
		// 	return candidates[i].Score > candidates[j].Score
		// })

		if settings.KNOCKOUTREPLAYS == nil || len(settings.KNOCKOUTREPLAYS) == 0 { // limit only with classic knockout
			candidates = candidates[:mutils.Min(len(candidates), settings.Knockout.MaxPlayers)]
		}
	}

	displayedMods := ^difficulty.ParseMods(settings.Knockout.HideMods)

	for i, replay := range candidates {
		log.Println(fmt.Sprintf("Loading replay for \"%s\":", replay.Username))

		control := NewSubControl()
		control.mods = difficulty.Modifier(replay.Mods)

		log.Println("\tMods:", control.mods.String())

		loadFrames(control, replay.ReplayData)

		mxCombo := replay.MaxCombo

		control.newHandling = replay.OsuVersion >= 20190506 // This was when slider scoring was changed, so *I think* replay handling as well: https://osu.ppy.sh/home/changelog/cuttingedge/20190506
		control.oldSpinners = replay.OsuVersion < 20190510  // This was when spinner scoring was changed: https://osu.ppy.sh/home/changelog/cuttingedge/20190510.2

		controller.replays = append(controller.replays, RpData{replay.Username + string(rune(unicode.MaxRune-i)), (control.mods & displayedMods).String(), control.mods, 100, 0, int64(mxCombo), osu.NONE, replay.ScoreID, replay.Timestamp})
		controller.controllers = append(controller.controllers, control)

		log.Println("\tExpected score:", replay.Score)
		log.Println("\tReplay loaded!")
	}

	if !localReplay && (settings.Knockout.AddDanser || len(candidates) == 0) {
		control := NewSubControl()
		control.mods = difficulty.Autoplay | beatMap.Diff.Mods

		control.danceController = NewGenericController()
		control.danceController.SetBeatMap(beatMap)

		controller.replays = append([]RpData{{settings.Knockout.DanserName, control.mods.String(), control.mods, 100, 0, 0, osu.NONE, -1, time.Now()}}, controller.replays...)
		controller.controllers = append([]*subControl{control}, controller.controllers...)

		if len(candidates) == 0 {
			controller.bMap.Diff.SetMods(controller.bMap.Diff.Mods | difficulty.Autoplay)
		}
	}

	settings.PLAYERS = len(controller.replays)
}

func organizeReplays() {
	replayDir := filepath.Join(env.DataDir(), replaysMaster)

	_ = godirwalk.Walk(replayDir, &godirwalk.Options{
		Callback: func(osPathname string, de *godirwalk.Dirent) error {
			if de.IsDir() && osPathname != replayDir {
				return godirwalk.SkipThis
			}

			if strings.HasSuffix(de.Name(), ".osr") {
				log.Println("Checking: ", osPathname)

				data, err := ioutil.ReadFile(osPathname)
				if err != nil {
					log.Println("Error reading file: ", err)
					log.Println("Skipping... ")
					return nil
				}

				replayD, err := rplpa.ParseReplay(data)
				if err != nil {
					log.Println("Error parsing file: ", err)
					log.Println("Skipping... ")
					return nil
				}

				err = os.MkdirAll(filepath.Join(replayDir, strings.ToLower(replayD.BeatmapMD5)), 0755)
				if err != nil {
					log.Println("Error creating directory: ", err)
					log.Println("Skipping... ")
					return nil
				}

				err = os.Rename(osPathname, filepath.Join(replayDir, strings.ToLower(replayD.BeatmapMD5), de.Name()))
				if err != nil {
					log.Println("Error moving file: ", err)
					log.Println("Skipping... ")
				}
			}

			return nil
		},
		Unsorted:            true,
		FollowSymbolicLinks: true,
	})
}

func (controller *ReplayController) getCandidates() (candidates []*rplpa.Replay) {
	excludedMods := difficulty.ParseMods(settings.Knockout.ExcludeMods)

	tryAddReplay := func(path string, modExclude bool) {
		log.Println("Loading: ", path)

		data, err := ioutil.ReadFile(path)
		if err != nil {
			panic(err)
		}

		replayD, err := rplpa.ParseReplay(data)

		if err != nil {
			log.Println("Failed to load replay:", err)
			return
		}

		// HACKHACK [xJunko]: yes.
		// if !strings.EqualFold(replayD.BeatmapMD5, controller.bMap.MD5) {
		// 	log.Println("Incompatible maps, skipping", replayD.Username)
		// 	return
		// }

		if !difficulty.Modifier(replayD.Mods).Compatible() || difficulty.Modifier(replayD.Mods).Active(difficulty.Target) {
			log.Println("Excluding for incompatible mods:", replayD.Username)
			return
		}

		if (replayD.Mods&uint32(excludedMods)) > 0 && modExclude {
			log.Println("Excluding for mods:", replayD.Username)
			return
		}

		if replayD.ReplayData == nil || len(replayD.ReplayData) == 0 {
			log.Println("Excluding for missing input data:", replayD.Username)
			return
		}

		candidates = append(candidates, replayD)
	}

	if settings.KNOCKOUTREPLAYS != nil && len(settings.KNOCKOUTREPLAYS) > 0 {
		sort.Slice(settings.KNOCKOUTREPLAYS, func(i, j int) bool {
			numI, errI := strconv.Atoi(settings.KNOCKOUTREPLAYS[i][:len(settings.KNOCKOUTREPLAYS[i])-len(".txt")]) // Extract numeric value from the first string
			numJ, errJ := strconv.Atoi(settings.KNOCKOUTREPLAYS[j][:len(settings.KNOCKOUTREPLAYS[j])-len(".txt")]) // Extract numeric value from the second string

			if errI == nil && errJ == nil {
				return numI < numJ
			}

			// If there was an error parsing the numeric value, fallback to lexicographic sorting
			return settings.KNOCKOUTREPLAYS[i] < settings.KNOCKOUTREPLAYS[j]
		})

		for _, r := range settings.KNOCKOUTREPLAYS {
			tryAddReplay(r, false)
		}
	} else {
		replayDir := filepath.Join(env.DataDir(), replaysMaster, controller.bMap.MD5)

		_ = godirwalk.Walk(replayDir, &godirwalk.Options{
			Callback: func(osPathname string, de *godirwalk.Dirent) error {
				if de.IsDir() && osPathname != replayDir {
					return godirwalk.SkipThis
				}

				if strings.HasSuffix(de.Name(), ".osr") {
					tryAddReplay(osPathname, true)
				}

				return nil
			},
			Unsorted:            false,
			FollowSymbolicLinks: true,
		})
	}

	return
}

func loadFrames(subController *subControl, frames []*rplpa.ReplayData) {
	// Remove mania seed frame if its present
	for i, frame := range frames {
		if frame.Time == -12345 {
			frames = append(frames[:i], frames[i+1:]...)
			break
		}
	}

	// Remove incorrect first frame if its delta is 0
	if frames[0].Time == 0 {
		frames = frames[1:]
	}

	times := make([]float64, 0, len(frames))

	duration := 0

	for _, frame := range frames {
		if frame.Time >= 0 {
			times = append(times, float64(frame.Time))
		}

		duration += int(frame.Time)
	}

	sort.Float64s(times)

	l := len(times)

	meanFrameTime := times[l/2]

	if l%2 == 0 {
		meanFrameTime = (times[l/2] + times[l/2-1]) / 2
	}

	diff := difficulty.NewDifficulty(5, 5, 5, 5)
	diff.SetMods(subController.mods)

	meanFrameTime = diff.GetModifiedTime(meanFrameTime)

	log.Println(fmt.Sprintf("\tMean cv frametime: %.2fms", meanFrameTime))

	if meanFrameTime <= 13 && !diff.CheckModActive(difficulty.Autoplay|difficulty.Relax|difficulty.Relax2) {
		log.Println("\tWARNING!!! THIS REPLAY WAS PROBABLY TIMEWARPED!!!")
	}

	log.Println(fmt.Sprintf("\tReplay duration: %dms", duration))

	subController.frames = frames
}

func (controller *ReplayController) InitCursors() {
	var modifiers []difficulty.Modifier

	for i, c := range controller.controllers {
		if controller.controllers[i].danceController != nil {
			controller.controllers[i].danceController.InitCursors()

			controller.controllers[i].danceController.GetCursors()[0].IsPlayer = true
			controller.controllers[i].danceController.GetCursors()[0].IsAutoplay = true

			cursors := controller.controllers[i].danceController.GetCursors()

			for _, cursor := range cursors {
				cursor.Name = controller.replays[i].Name
				cursor.ScoreTime = time.Now()
				cursor.ScoreID = -1
			}

			controller.cursors = append(controller.cursors, cursors...)
		} else {
			cursor := graphics.NewCursor()
			cursor.Name = controller.replays[i].Name
			cursor.ScoreID = controller.replays[i].scoreID
			cursor.ScoreTime = controller.replays[i].ScoreTime
			cursor.OldSpinnerScoring = controller.controllers[i].oldSpinners
			cursor.IsReplay = true

			cursor.SetPos(vector.NewVec2f(c.frames[0].MouseX, c.frames[0].MouseY))
			cursor.Update(0)

			c.replayTime += c.frames[0].Time
			c.frames = c.frames[1:]

			controller.cursors = append(controller.cursors, cursor)
		}

		if controller.bMap.Diff.Mods.Active(difficulty.HardRock) != controller.replays[i].ModsV.Active(difficulty.HardRock) {
			controller.cursors[i].InvertDisplay = true
		}

		modifiers = append(modifiers, controller.replays[i].ModsV)
	}

	controller.ruleset = osu.NewOsuRuleset(controller.bMap, controller.cursors, modifiers)

	for i := range controller.controllers {
		if controller.replays[i].ModsV.Active(difficulty.Relax) {
			controller.controllers[i].relaxController = input.NewRelaxInputProcessor(controller.ruleset, controller.cursors[i])
		}

		if controller.replays[i].ModsV.Active(difficulty.Relax2) {
			controller.controllers[i].mouseController = schedulers.NewGenericScheduler(movers.NewLinearMoverSimple, 0, 0)

			diff := difficulty.NewDifficulty(controller.bMap.Diff.GetHP(), controller.bMap.Diff.GetCS(), controller.bMap.Diff.GetOD(), controller.bMap.Diff.GetAR())
			diff.SetMods(controller.replays[i].ModsV)
			diff.SetCustomSpeed(controller.bMap.Diff.CustomSpeed)

			controller.controllers[i].mouseController.Init(controller.bMap.GetObjectsCopy(), diff, controller.cursors[i], spinners.GetMoverCtorByName("circle"), false)
		}
	}
}

func (controller *ReplayController) Update(time float64, delta float64) {
	numSkipped := int(time-controller.lastTime) - 1

	if numSkipped >= 1 {
		for nTime := numSkipped; nTime >= 1; nTime-- {
			controller.updateMain(time - float64(nTime))
		}
	}

	controller.updateMain(time)

	for i := range controller.controllers {
		if controller.controllers[i].danceController == nil {
			controller.cursors[i].Update(delta)
		}

		sc := controller.ruleset.GetScore(controller.cursors[i])
		controller.replays[i].Accuracy = sc.Accuracy
		controller.replays[i].Combo = int64(sc.Combo)
		controller.replays[i].Grade = sc.Grade
	}
}

func (controller *ReplayController) updateMain(nTime float64) {
	controller.bMap.Update(nTime)

	for i, c := range controller.controllers {
		if c.danceController != nil {
			c.danceController.Update(nTime, nTime-controller.lastTime)

			if int64(nTime)%17 == 0 {
				controller.cursors[i].LastFrameTime = int64(nTime) - 17
				controller.cursors[i].CurrentFrameTime = int64(nTime)
				controller.cursors[i].IsReplayFrame = true
			} else {
				controller.cursors[i].IsReplayFrame = false
			}

			if int64(nTime) != c.lastTime {
				controller.ruleset.UpdateClickFor(controller.cursors[i], int64(nTime))
				controller.ruleset.UpdateNormalFor(controller.cursors[i], int64(nTime), false)
				controller.ruleset.UpdatePostFor(controller.cursors[i], int64(nTime), false)
			}

			c.lastTime = int64(nTime)
		} else {
			wasUpdated := false

			isRelax := (controller.replays[i].ModsV & difficulty.Relax) > 0
			isAutopilot := (controller.replays[i].ModsV & difficulty.Relax2) > 0

			if isAutopilot {
				c.mouseController.Update(nTime)
			}

			if c.replayIndex < len(c.frames) {
				for c.replayIndex < len(c.frames) && c.replayTime+c.frames[c.replayIndex].Time <= int64(nTime) {
					frame := c.frames[c.replayIndex]
					c.replayTime += frame.Time

					// If next frame is not in the next millisecond, assume it's -36ms slider end
					processAhead := true
					if c.replayIndex+1 < len(c.frames) && c.frames[c.replayIndex+1].Time == 1 {
						processAhead = false
					}

					if !isAutopilot {
						controller.cursors[i].SetPos(vector.NewVec2f(frame.MouseX, frame.MouseY))
					}

					controller.cursors[i].LastFrameTime = controller.cursors[i].CurrentFrameTime
					controller.cursors[i].CurrentFrameTime = c.replayTime
					controller.cursors[i].IsReplayFrame = true

					if !isRelax {
						controller.cursors[i].LeftKey = frame.KeyPressed.LeftClick && frame.KeyPressed.Key1
						controller.cursors[i].RightKey = frame.KeyPressed.RightClick && frame.KeyPressed.Key2

						controller.cursors[i].LeftMouse = frame.KeyPressed.LeftClick && !frame.KeyPressed.Key1
						controller.cursors[i].RightMouse = frame.KeyPressed.RightClick && !frame.KeyPressed.Key2

						controller.cursors[i].LeftButton = frame.KeyPressed.LeftClick
						controller.cursors[i].RightButton = frame.KeyPressed.RightClick
					} else {
						c.relaxController.Update(float64(c.replayTime))
					}

					controller.cursors[i].SmokeKey = frame.KeyPressed.Smoke

					controller.ruleset.UpdateClickFor(controller.cursors[i], c.replayTime)
					controller.ruleset.UpdateNormalFor(controller.cursors[i], c.replayTime, processAhead)

					// New replays (after 20190506) scores object ends only on replay frame
					if c.newHandling || c.replayIndex == len(c.frames)-1 {
						controller.ruleset.UpdatePostFor(controller.cursors[i], c.replayTime, processAhead)
					} else {
						localIndex := mutils.Clamp(c.replayIndex+1, 0, len(c.frames)-1)
						localFrame := c.frames[localIndex]

						// HACK for older replays: update object ends till the next frame
						for localTime := c.replayTime; localTime < c.replayTime+localFrame.Time; localTime++ {
							controller.ruleset.UpdatePostFor(controller.cursors[i], localTime, false)
						}
					}

					wasUpdated = true

					c.replayIndex++
				}

				if !wasUpdated {
					if !isAutopilot {
						localIndex := mutils.Clamp(c.replayIndex, 0, len(c.frames)-1)

						progress := math32.Min(float32(nTime-float64(c.replayTime)), float32(c.frames[localIndex].Time)) / float32(c.frames[localIndex].Time)

						prevIndex := mutils.Max(0, localIndex-1)

						mX := (c.frames[localIndex].MouseX-c.frames[prevIndex].MouseX)*progress + c.frames[prevIndex].MouseX
						mY := (c.frames[localIndex].MouseY-c.frames[prevIndex].MouseY)*progress + c.frames[prevIndex].MouseY

						controller.cursors[i].SetPos(vector.NewVec2f(mX, mY))
					}

					controller.cursors[i].IsReplayFrame = false
				}

				if c.replayIndex >= len(c.frames) {
					controller.ruleset.PlayerStopped(controller.cursors[i], c.replayTime)
				}
			} else {
				controller.cursors[i].LeftKey = false
				controller.cursors[i].RightKey = false
				controller.cursors[i].LeftMouse = false
				controller.cursors[i].RightMouse = false
				controller.cursors[i].LeftButton = false
				controller.cursors[i].RightButton = false

				controller.ruleset.UpdateClickFor(controller.cursors[i], int64(nTime))
				controller.ruleset.UpdateNormalFor(controller.cursors[i], int64(nTime), false)
				controller.ruleset.UpdatePostFor(controller.cursors[i], int64(nTime), false)
			}
		}
	}

	if int64(nTime) != int64(controller.lastTime) {
		controller.ruleset.Update(int64(nTime))
	}

	controller.lastTime = nTime
}

func (controller *ReplayController) GetCursors() []*graphics.Cursor {
	return controller.cursors
}

func (controller *ReplayController) GetReplays() []RpData {
	return controller.replays
}

func (controller *ReplayController) GetRuleset() *osu.OsuRuleSet {
	return controller.ruleset
}

func (controller *ReplayController) GetBeatMap() *beatmap.BeatMap {
	return controller.bMap
}

func (controller *ReplayController) GetClick(player, key int) bool {
	switch key {
	case 0:
		return controller.cursors[player].LeftKey
	case 1:
		return controller.cursors[player].RightKey
	case 2:
		return controller.cursors[player].LeftMouse
	case 3:
		return controller.cursors[player].RightMouse
	}

	return false
}
