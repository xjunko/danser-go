package states

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/wieku/danser-go/app/audio"
	"github.com/wieku/danser-go/app/beatmap"
	"github.com/wieku/danser-go/app/beatmap/difficulty"
	camera2 "github.com/wieku/danser-go/app/bmath/camera"
	"github.com/wieku/danser-go/app/dance"
	"github.com/wieku/danser-go/app/discord"
	"github.com/wieku/danser-go/app/graphics"
	"github.com/wieku/danser-go/app/input"
	"github.com/wieku/danser-go/app/rulesets/osu"
	"github.com/wieku/danser-go/app/settings"
	"github.com/wieku/danser-go/app/states/components/common"
	"github.com/wieku/danser-go/app/states/components/containers"
	"github.com/wieku/danser-go/app/states/components/overlays"
	"github.com/wieku/danser-go/app/utils"
	"github.com/wieku/danser-go/framework/bass"
	"github.com/wieku/danser-go/framework/frame"
	"github.com/wieku/danser-go/framework/goroutines"
	batch2 "github.com/wieku/danser-go/framework/graphics/batch"
	"github.com/wieku/danser-go/framework/graphics/effects"
	"github.com/wieku/danser-go/framework/graphics/font"
	"github.com/wieku/danser-go/framework/graphics/texture"
	"github.com/wieku/danser-go/framework/math/animation"
	"github.com/wieku/danser-go/framework/math/animation/easing"
	color2 "github.com/wieku/danser-go/framework/math/color"
	"github.com/wieku/danser-go/framework/math/mutils"
	"github.com/wieku/danser-go/framework/math/scaling"
	"github.com/wieku/danser-go/framework/math/vector"
	"github.com/wieku/danser-go/framework/qpc"
	"github.com/wieku/danser-go/framework/statistic"
)

const windowsOffset = 15

type Player struct {
	font        *font.Font
	bMap        *beatmap.BeatMap
	bloomEffect *effects.BloomEffect

	lastTime        int64
	lastMusicPos    float64
	lastProgressMsF float64
	progressMsF     float64
	rawPositionF    float64
	progressMs      int64

	batch       *batch2.QuadBatch
	controller  dance.Controller
	background  *common.Background
	BgScl       vector.Vector2d
	Scl         float64
	SclA        float64
	fadeOut     float64
	fadeIn      float64
	start       bool
	musicPlayer bass.ITrack
	profiler    *frame.Counter
	profilerU   *frame.Counter

	mainCamera   *camera2.Camera
	objectCamera *camera2.Camera
	bgCamera     *camera2.Camera
	uiCamera     *camera2.Camera

	dimGlider       *animation.Glider
	blurGlider      *animation.Glider
	fxGlider        *animation.Glider
	cursorGlider    *animation.Glider
	counter         float64
	storyboardDrawn int
	mapFullName     string
	Epi             *texture.TextureRegion
	epiGlider       *animation.Glider
	overlay         overlays.Overlay
	blur            *effects.BlurEffect

	coin *common.DanserCoin

	hudGlider *animation.Glider

	volumeGlider    *animation.Glider
	speedGlider     *animation.Glider
	pitchGlider     *animation.Glider
	frequencyGlider *animation.Glider

	startPoint  float64
	startPointE float64

	baseLimit     int
	updateLimiter *frame.Limiter

	objectsAlpha    *animation.Glider
	objectContainer *containers.HitObjectContainer

	MapEnd      float64
	RunningTime float64

	startOffset float64
	lateStart   bool
	mapEndL     float64

	ScaledWidth  float64
	ScaledHeight float64

	nightcore *common.NightcoreProcessor

	realTime         float64
	objectsAlphaFail *animation.Glider
	failOX           *animation.Glider
	failOY           *animation.Glider
	failRotation     *animation.Glider

	failing bool
	failAt  float64
	failed  bool
}

func NewPlayer(beatMap *beatmap.BeatMap) *Player {
	player := new(Player)

	graphics.LoadTextures()

	if settings.Graphics.Experimental.UsePersistentBuffers {
		player.batch = batch2.NewQuadBatchPersistent()
	} else {
		player.batch = batch2.NewQuadBatch()
	}

	player.font = font.GetFont("Quicksand Bold")

	discord.SetMap(beatMap.Artist, beatMap.Name, beatMap.Difficulty)

	player.bMap = beatMap
	player.mapFullName = fmt.Sprintf("%s - %s [%s]", beatMap.Artist, beatMap.Name, beatMap.Difficulty)
	log.Println("Playing:", player.mapFullName)

	track := bass.NewTrack(filepath.Join(settings.General.GetSongsDir(), beatMap.Dir, beatMap.Audio))

	if track == nil {
		log.Println("Failed to create music stream, creating a dummy stream...")

		player.musicPlayer = bass.NewTrackVirtual(beatMap.HitObjects[len(beatMap.HitObjects)-1].GetEndTime()/1000 + 1)
	} else {
		log.Println("Audio track:", beatMap.Audio)

		player.musicPlayer = track
	}

	var err error
	player.Epi, err = utils.LoadTextureToAtlas(graphics.Atlas, "assets/textures/warning.png")

	if err != nil {
		log.Println(err)
	}

	settings.START = math.Min(settings.START, (beatMap.HitObjects[len(beatMap.HitObjects)-1].GetStartTime()-1)/1000) // cap start to start time of the last HitObject - 1ms

	if (settings.START > 0.01 || !math.IsInf(settings.END, 1)) && (settings.PLAY || !settings.KNOCKOUT) {
		scrub := math.Max(0, settings.START*1000)
		end := settings.END * 1000

		removed := false

		for i := 0; i < len(beatMap.HitObjects); i++ {
			o := beatMap.HitObjects[i]
			if o.GetStartTime() > scrub && end > o.GetEndTime() {
				continue
			}

			beatMap.HitObjects = append(beatMap.HitObjects[:i], beatMap.HitObjects[i+1:]...)
			i--

			removed = true
		}

		for i := 0; i < len(beatMap.HitObjects); i++ {
			beatMap.HitObjects[i].SetID(int64(i))
		}

		for i := 0; i < len(beatMap.Pauses); i++ {
			o := beatMap.Pauses[i]
			if o.GetStartTime() > scrub && end > o.GetEndTime() {
				continue
			}

			beatMap.Pauses = append(beatMap.Pauses[:i], beatMap.Pauses[i+1:]...)
			i--
		}

		if removed && settings.START > 0.01 {
			settings.START = 0
			settings.SKIP = true
		}
	}

	player.background = common.NewBackground(true)
	player.background.SetBeatmap(beatMap, true, true)

	player.mainCamera = camera2.NewCamera()
	player.mainCamera.SetOsuViewport(int(settings.Graphics.GetWidth()), int(settings.Graphics.GetHeight()), settings.Playfield.Scale, true, settings.Playfield.OsuShift)
	player.mainCamera.Update()

	player.objectCamera = camera2.NewCamera()
	player.objectCamera.SetOsuViewport(int(settings.Graphics.GetWidth()), int(settings.Graphics.GetHeight()), settings.Playfield.Scale, true, settings.Playfield.OsuShift)
	player.objectCamera.Update()

	player.bgCamera = camera2.NewCamera()

	sbScale := 1.0
	if settings.Playfield.ScaleStoryboardWithPlayfield {
		sbScale = settings.Playfield.Scale
	}

	player.bgCamera.SetOsuViewport(int(settings.Graphics.GetWidth()), int(settings.Graphics.GetHeight()), sbScale, !settings.Playfield.OsuShift && settings.Playfield.MoveStoryboardWithPlayfield, false)
	player.bgCamera.Update()

	player.ScaledHeight = 1080.0
	player.ScaledWidth = player.ScaledHeight * settings.Graphics.GetAspectRatio()

	player.uiCamera = camera2.NewCamera()
	player.uiCamera.SetViewport(int(player.ScaledWidth), int(player.ScaledHeight), true)
	player.uiCamera.SetViewportF(0, int(player.ScaledHeight), int(player.ScaledWidth), 0)
	player.uiCamera.Update()

	graphics.Camera = player.mainCamera

	player.bMap.Reset()

	if settings.PLAY {
		player.controller = dance.NewPlayerController()

		player.controller.SetBeatMap(player.bMap)
		player.controller.InitCursors()
		player.overlay = overlays.NewScoreOverlay(player.controller.(*dance.PlayerController).GetRuleset(), player.controller.GetCursors()[0])
	} else if settings.KNOCKOUT {
		controller := dance.NewReplayController()
		player.controller = controller

		player.controller.SetBeatMap(player.bMap)
		player.controller.InitCursors()

		if settings.PLAYERS == 1 {
			player.overlay = overlays.NewScoreOverlay(player.controller.(*dance.ReplayController).GetRuleset(), player.controller.GetCursors()[0])
		} else {
			player.overlay = overlays.NewKnockoutOverlay(controller.(*dance.ReplayController))
		}
	} else {
		player.controller = dance.NewGenericController()
		player.controller.SetBeatMap(player.bMap)
		player.controller.InitCursors()
	}

	player.lastTime = -1

	player.objectContainer = containers.NewHitObjectContainer(beatMap)

	player.Scl = 1
	player.fadeOut = 1.0
	player.fadeIn = 0.0

	player.volumeGlider = animation.NewGlider(1)
	player.speedGlider = animation.NewGlider(settings.SPEED)
	player.pitchGlider = animation.NewGlider(settings.PITCH)
	player.frequencyGlider = animation.NewGlider(1)

	player.hudGlider = animation.NewGlider(0)
	player.hudGlider.SetEasing(easing.OutQuad)

	player.dimGlider = animation.NewGlider(0)
	player.dimGlider.SetEasing(easing.OutQuad)

	player.blurGlider = animation.NewGlider(0)
	player.blurGlider.SetEasing(easing.OutQuad)

	player.fxGlider = animation.NewGlider(0)
	player.cursorGlider = animation.NewGlider(0)
	player.epiGlider = animation.NewGlider(0)
	player.objectsAlpha = animation.NewGlider(1)

	player.objectsAlphaFail = animation.NewGlider(1)
	player.failOX = animation.NewGlider(0)
	player.failOY = animation.NewGlider(0)
	player.failRotation = animation.NewGlider(0)

	player.trySetupFail()

	preempt := math.Min(1800, beatMap.Diff.Preempt)

	skipTime := 0.0
	if settings.SKIP {
		skipTime = beatMap.HitObjects[0].GetStartTime()
	}

	skipTime = math.Max(skipTime, settings.START*1000) - preempt

	beatmapStart := math.Max(beatMap.HitObjects[0].GetStartTime(), settings.START*1000) - preempt
	beatmapEnd := beatMap.HitObjects[len(beatMap.HitObjects)-1].GetEndTime() + float64(beatMap.Diff.Hit50)

	if !math.IsInf(settings.END, 1) {
		end := settings.END * 1000
		beatmapEnd = math.Min(end, beatMap.HitObjects[len(beatMap.HitObjects)-1].GetEndTime()) + float64(beatMap.Diff.Hit50)
	}

	startOffset := 0.0

	if math.Max(0, skipTime) > 0.01 {
		startOffset = skipTime
		player.startPoint = math.Max(0, startOffset)

		for _, o := range beatMap.HitObjects {
			if o.GetStartTime() > player.startPoint {
				break
			}

			o.DisableAudioSubmission(true)
		}

		player.volumeGlider.SetValue(0.0)
		player.volumeGlider.AddEvent(skipTime, skipTime+beatMap.Diff.TimeFadeIn, 1.0)

		player.objectsAlpha.SetValue(0.0)
		player.objectsAlpha.AddEvent(skipTime, skipTime+beatMap.Diff.TimeFadeIn, 1.0)

		if player.overlay != nil {
			player.overlay.DisableAudioSubmission(true)
		}

		for i := -1000.0; i < startOffset; i += 1.0 {
			player.controller.Update(i, 1)

			if player.overlay != nil {
				player.overlay.Update(i)
			}
		}

		if player.overlay != nil {
			player.overlay.DisableAudioSubmission(false)
		}

		player.lateStart = true
	} else {
		startOffset = -preempt
	}

	player.startPointE = startOffset

	startOffset += -settings.Playfield.LeadInHold * 1000

	player.dimGlider.AddEvent(startOffset-500, startOffset, 1.0-settings.Playfield.Background.Dim.Intro)
	player.blurGlider.AddEvent(startOffset-500, startOffset, settings.Playfield.Background.Blur.Values.Intro)
	player.fxGlider.AddEvent(startOffset-500, startOffset, 1.0-settings.Playfield.Logo.Dim.Intro)
	player.hudGlider.AddEvent(startOffset-500, startOffset, 1.0)

	if _, ok := player.overlay.(*overlays.ScoreOverlay); ok {
		player.cursorGlider.AddEvent(startOffset-750, startOffset-250, 1.0)
	} else {
		player.cursorGlider.AddEvent(beatmapStart-750, beatmapStart-250, 1.0)
	}

	player.dimGlider.AddEvent(beatmapStart, beatmapStart+1000, 1.0-settings.Playfield.Background.Dim.Normal)
	player.blurGlider.AddEvent(beatmapStart, beatmapStart+1000, settings.Playfield.Background.Blur.Values.Normal)
	player.fxGlider.AddEvent(beatmapStart, beatmapStart+1000, 1.0-settings.Playfield.Logo.Dim.Normal)

	fadeOut := settings.Playfield.FadeOutTime * 1000

	if s, ok := player.overlay.(*overlays.ScoreOverlay); ok {
		if settings.Gameplay.ShowResultsScreen {
			beatmapEnd += 1000
			fadeOut = 250
		}

		s.SetBeatmapEnd(beatmapEnd + 3000 + fadeOut)
	}

	beatmapEnd += 5000

	if !math.IsInf(settings.END, 1) {
		for _, o := range beatMap.HitObjects {
			if o.GetEndTime() <= beatmapEnd {
				continue
			}

			o.DisableAudioSubmission(true)
		}

		if !settings.PLAY {
			player.objectsAlpha.AddEvent(beatmapEnd, beatmapEnd+fadeOut, 0)
		}
	}

	player.dimGlider.AddEvent(beatmapEnd, beatmapEnd+fadeOut, 0.0)
	player.fxGlider.AddEvent(beatmapEnd, beatmapEnd+fadeOut, 0.0)
	player.cursorGlider.AddEvent(beatmapEnd, beatmapEnd+fadeOut, 0.0)
	player.hudGlider.AddEvent(beatmapEnd, beatmapEnd+fadeOut, 0.0)

	player.mapEndL = beatmapEnd + fadeOut
	player.MapEnd = beatmapEnd + fadeOut

	if _, ok := player.overlay.(*overlays.ScoreOverlay); ok && settings.Gameplay.ShowResultsScreen {
		player.speedGlider.AddEvent(beatmapEnd+fadeOut, beatmapEnd+fadeOut, 1)
		player.pitchGlider.AddEvent(beatmapEnd+fadeOut, beatmapEnd+fadeOut, 1)

		player.MapEnd += (settings.Gameplay.ResultsScreenTime + 1) * 1000
		if player.MapEnd < player.musicPlayer.GetLength()*1000 {
			player.volumeGlider.AddEvent(player.MapEnd-settings.Gameplay.ResultsScreenTime*1000-500, player.MapEnd, 0.0)
		}
	} else {
		player.volumeGlider.AddEvent(beatmapEnd, beatmapEnd+fadeOut, 0.0)
	}

	player.MapEnd += 100

	// See https://github.com/Wieku/danser-go/issues/121
	player.musicPlayer.AddSilence(math.Max(0, player.MapEnd/1000-player.musicPlayer.GetLength()))

	if settings.Playfield.SeizureWarning.Enabled {
		am := math.Max(1000, settings.Playfield.SeizureWarning.Duration*1000)
		startOffset -= am
		player.epiGlider.AddEvent(startOffset, startOffset+500, 1.0)
		player.epiGlider.AddEvent(startOffset+am-500, startOffset+am, 0.0)
	}

	startOffset -= math.Max(settings.Playfield.LeadInTime*1000, 1000)

	player.startOffset = startOffset
	player.progressMsF = startOffset
	player.rawPositionF = startOffset

	player.RunningTime = player.MapEnd - startOffset

	for _, p := range beatMap.Pauses {
		startTime := p.GetStartTime()
		endTime := p.GetEndTime()

		if endTime-startTime < 1000*settings.SPEED || endTime < player.startPoint || startTime > player.MapEnd {
			continue
		}

		player.dimGlider.AddEvent(startTime, startTime+1000*settings.SPEED, 1.0-settings.Playfield.Background.Dim.Breaks)
		player.blurGlider.AddEvent(startTime, startTime+1000*settings.SPEED, settings.Playfield.Background.Blur.Values.Breaks)
		player.fxGlider.AddEvent(startTime, startTime+1000*settings.SPEED, 1.0-settings.Playfield.Logo.Dim.Breaks)

		if !settings.Cursor.ShowCursorsOnBreaks {
			player.cursorGlider.AddEvent(startTime, startTime+100*settings.SPEED, 0.0)
		}

		player.dimGlider.AddEvent(endTime, endTime+1000*settings.SPEED, 1.0-settings.Playfield.Background.Dim.Normal)
		player.blurGlider.AddEvent(endTime, endTime+1000*settings.SPEED, settings.Playfield.Background.Blur.Values.Normal)
		player.fxGlider.AddEvent(endTime, endTime+1000*settings.SPEED, 1.0-settings.Playfield.Logo.Dim.Normal)
		player.cursorGlider.AddEvent(endTime, endTime+1000*settings.SPEED, 1.0)
	}

	player.background.SetTrack(player.musicPlayer)

	player.coin = common.NewDanserCoin()
	player.coin.SetMap(beatMap, player.musicPlayer)

	player.coin.SetScale(0.25 * math.Min(settings.Graphics.GetWidthF(), settings.Graphics.GetHeightF()))

	player.profiler = frame.NewCounter()

	player.bloomEffect = effects.NewBloomEffect(int(settings.Graphics.GetWidth()), int(settings.Graphics.GetHeight()))
	player.blur = effects.NewBlurEffect(int(settings.Graphics.GetWidth()), int(settings.Graphics.GetHeight()))

	player.background.Update(player.progressMsF, settings.Graphics.GetWidthF()/2, settings.Graphics.GetHeightF()/2)

	player.profilerU = frame.NewCounter()

	player.baseLimit = 1000

	player.updateLimiter = frame.NewLimiter(player.baseLimit)

	if player.bMap.Diff.CheckModActive(difficulty.Nightcore) {
		player.nightcore = common.NewNightcoreProcessor()
		player.nightcore.SetMap(player.bMap, player.musicPlayer)
	}

	if settings.RECORD {
		return player
	}

	goroutines.RunOS(func() {
		var lastTimeNano = qpc.GetNanoTime()

		for !input.Win.ShouldClose() {
			currentTimeNano := qpc.GetNanoTime()

			delta := float64(currentTimeNano-lastTimeNano) / 1000000.0

			player.profilerU.PutSample(delta)

			musicState := player.musicPlayer.GetState()

			speed := 1.0

			if musicState == bass.MusicStopped {
				if player.rawPositionF < player.startPointE || player.start {
					player.rawPositionF += delta
				} else {
					speed = settings.SPEED
					player.rawPositionF += delta * speed
				}
			} else {
				musicPos := player.musicPlayer.GetPosition() * 1000
				speed = player.musicPlayer.GetTempo()

				if musicPos != player.lastMusicPos || musicState == bass.MusicPaused {
					player.rawPositionF = musicPos
					player.lastMusicPos = musicPos
				} else if musicPos > 1 {
					// In DirectSound mode with VistaTruePos set to FALSE music is reported at 10ms intervals so we need to *interpolate* it
					// Wait at least 1ms because before interpolating because there's a 60ish ms delay before music in playing state starts reporting time and we don't want to jump back in time
					player.rawPositionF += delta * speed
				}
			}

			platformOffset := 0.0
			if runtime.GOOS == "windows" { // For some reason WASAPI reports time with 15ms delay, so we need to correct it
				platformOffset = windowsOffset
			}

			oldOffset := 0.0
			if player.bMap.Version < 5 {
				oldOffset = -24
			}

			player.progressMsF = player.rawPositionF + (platformOffset+float64(settings.Audio.Offset)+float64(settings.LOCALOFFSET))*speed + oldOffset

			player.updateMain(delta)

			lastTimeNano = currentTimeNano

			player.updateLimiter.Sync()
		}

		player.musicPlayer.Stop()
		bass.StopLoops()
	})

	return player
}

func (player *Player) trySetupFail() {
	if sO, ok := player.overlay.(*overlays.ScoreOverlay); ok {
		var ruleset *osu.OsuRuleSet

		if rC, ok1 := player.controller.(*dance.ReplayController); ok1 {
			ruleset = rC.GetRuleset()
		} else if rP, ok2 := player.controller.(*dance.PlayerController); ok2 {
			ruleset = rP.GetRuleset()
		}

		if ruleset != nil {
			ruleset.SetFailListener(func(cursor *graphics.Cursor) {
				if !settings.RECORD {
					audio.PlayFailSound()
				}

				log.Println("Player failed!")

				sO.Fail(true)

				player.frequencyGlider.AddEvent(player.realTime, player.realTime+2400, 0.0)
				player.objectsAlphaFail.AddEvent(player.realTime, player.realTime+2400, 0.0)

				player.failOX.AddEvent(player.realTime, player.realTime+2400, camera2.OsuWidth*(rand.Float64()-0.5)/2)
				player.failOY.AddEvent(player.realTime, player.realTime+2400, -camera2.OsuHeight*(1+rand.Float64()*0.2))

				rotBase := rand.Float64()

				player.failRotation.AddEvent(player.realTime, player.realTime+2400, math.Copysign((math.Abs(rotBase)*0.5+0.5)/6*math.Pi, rotBase))

				player.failing = true
				player.failAt = player.realTime + 2400

				player.dimGlider.Reset()
				player.blurGlider.Reset()
				player.hudGlider.Reset()
				player.fxGlider.Reset()
				player.cursorGlider.Reset()
				player.objectsAlpha.Reset()
			})
		}

		if player.controller.GetCursors()[0].IsPlayer && !player.controller.GetCursors()[0].IsAutoplay {
			player.cursorGlider.SetValue(1.0)
		}
	}
}

func (player *Player) Update(delta float64) bool {
	speed := 1.0

	if player.musicPlayer.GetState() == bass.MusicPlaying {
		speed = player.musicPlayer.GetTempo() * player.musicPlayer.GetRelativeFrequency()
	} else if !(player.progressMsF < player.startPointE || player.start) {
		speed = settings.SPEED
	}

	player.rawPositionF += delta * speed

	oldOffset := 0.0
	if player.bMap.Version < 5 {
		oldOffset = -24
	}

	player.progressMsF = player.rawPositionF + float64(settings.LOCALOFFSET)*speed + oldOffset

	player.updateMain(delta)

	if player.progressMsF >= player.MapEnd {
		player.musicPlayer.Stop()
		bass.StopLoops()

		return true
	}

	return false
}

func (player *Player) GetTime() float64 {
	return player.progressMsF
}

func (player *Player) GetTimeOffset() float64 {
	return player.progressMsF - player.startOffset
}

func (player *Player) updateMain(delta float64) {
	player.realTime += delta

	if player.rawPositionF >= player.startPoint && !player.start {
		player.musicPlayer.Play()

		if player.overlay != nil {
			player.overlay.SetMusic(player.musicPlayer)
		}

		player.musicPlayer.SetPosition(player.startPoint / 1000)

		discord.SetDuration(int64((player.mapEndL-player.musicPlayer.GetPosition()*1000)/settings.SPEED + (player.MapEnd - player.mapEndL)))

		if player.overlay == nil {
			discord.UpdateDance(settings.TAG, settings.DIVIDES)
		}

		player.start = true
	}

	player.speedGlider.Update(player.progressMsF)
	player.pitchGlider.Update(player.progressMsF)
	player.frequencyGlider.Update(player.realTime)

	player.objectsAlphaFail.Update(player.realTime)
	player.failOX.Update(player.realTime)
	player.failOY.Update(player.realTime)
	player.failRotation.Update(player.realTime)

	player.objectCamera.SetOrigin(vector.NewVec2d(player.failOX.GetValue(), player.failOY.GetValue()))
	player.objectCamera.SetRotation(player.failRotation.GetValue())
	player.objectCamera.Update()

	if player.failing && player.realTime >= player.failAt {
		if !player.failed {
			player.musicPlayer.Pause()
			player.MapEnd = player.progressMsF
		}

		player.failed = true
	}

	player.musicPlayer.SetTempo(player.speedGlider.GetValue())
	player.musicPlayer.SetPitch(player.pitchGlider.GetValue())
	player.musicPlayer.SetRelativeFrequency(player.frequencyGlider.GetValue())

	if player.progressMsF >= player.startPointE {
		if _, ok := player.controller.(*dance.GenericController); ok {
			player.bMap.Update(player.progressMsF)
		}

		player.objectContainer.Update(player.progressMsF)
	}

	if player.progressMsF >= player.startPointE || settings.PLAY {
		if player.progressMsF < player.mapEndL {
			player.controller.Update(player.progressMsF, delta)

			if player.nightcore != nil {
				player.nightcore.Update(player.progressMsF)
			}
		} else if settings.Gameplay.ShowResultsScreen {
			if player.overlay != nil {
				player.overlay.DisableAudioSubmission(true)
			}
			player.controller.Update(player.bMap.HitObjects[len(player.bMap.HitObjects)-1].GetEndTime()+float64(player.bMap.Diff.Hit50)+100, delta)
		}

		if player.lateStart {
			if player.overlay != nil {
				player.overlay.Update(player.progressMsF)
			}
		}
	}

	if player.overlay != nil && !player.lateStart {
		player.overlay.Update(player.progressMsF)
	}

	player.updateMusic(delta)

	player.coin.Update(player.progressMsF)
	player.coin.SetAlpha(float32(player.fxGlider.GetValue()))

	var offset vector.Vector2d

	for _, c := range player.controller.GetCursors() {
		offset = offset.Add(player.mainCamera.Project(c.Position.Copy64()).Mult(vector.NewVec2d(2/settings.Graphics.GetWidthF(), -2/settings.Graphics.GetHeightF())))
	}

	offset = offset.Scl(1 / float64(len(player.controller.GetCursors())))

	player.background.Update(player.progressMsF, offset.X*player.cursorGlider.GetValue(), offset.Y*player.cursorGlider.GetValue())

	player.epiGlider.Update(player.progressMsF)
	player.dimGlider.Update(player.progressMsF)
	player.blurGlider.Update(player.progressMsF)
	player.fxGlider.Update(player.progressMsF)
	player.cursorGlider.Update(player.progressMsF)
	player.hudGlider.Update(player.progressMsF)
	player.volumeGlider.Update(player.progressMsF)
	player.objectsAlpha.Update(player.progressMsF)

	if player.musicPlayer.GetState() == bass.MusicPlaying {
		player.musicPlayer.SetVolumeRelative(player.volumeGlider.GetValue())
	}
}

func (player *Player) updateMusic(delta float64) {
	player.musicPlayer.Update()

	target := mutils.ClampF(player.musicPlayer.GetBoost()*(settings.Audio.BeatScale-1.0)+1.0, 1.0, settings.Audio.BeatScale)

	if settings.Audio.BeatUseTimingPoints {
		player.Scl = 1 + player.coin.Beat*(settings.Audio.BeatScale-1.0)
	} else if player.Scl < target {
		player.Scl += (target - player.Scl) * 0.3 * delta / 16.66667
	} else if player.Scl > target {
		player.Scl -= (player.Scl - target) * 0.15 * delta / 16.66667
	}
}

func (player *Player) Draw(float64) {
	if player.lastTime <= 0 {
		player.lastTime = qpc.GetNanoTime()
	}

	tim := qpc.GetNanoTime()
	timMs := float64(tim-player.lastTime) / 1000000.0

	fps := player.profiler.GetFPS()

	player.updateLimiter.FPS = mutils.Clamp(int(fps*1.2), player.baseLimit, 10000)

	if player.background.GetStoryboard() != nil {
		player.background.GetStoryboard().SetFPS(mutils.Clamp(int(fps*1.2), player.baseLimit, 10000))
	}

	if fps > 58 && timMs > 18 && !settings.RECORD {
		log.Println(fmt.Sprintf("Slow frame detected! Frame time: %.3fms | Av. frame time: %.3fms", timMs, 1000.0/fps))
	}

	player.progressMs = int64(player.progressMsF)

	player.profiler.PutSample(timMs)
	player.lastTime = tim

	objectCameras := player.objectCamera.GenRotated(settings.DIVIDES, -2*math.Pi/float64(settings.DIVIDES))
	cursorCameras := player.mainCamera.GenRotated(settings.DIVIDES, -2*math.Pi/float64(settings.DIVIDES))

	bgAlpha := player.dimGlider.GetValue()
	if settings.Playfield.Background.FlashToTheBeat {
		bgAlpha = mutils.ClampF(bgAlpha*player.Scl, 0, 1)
	}

	player.background.Draw(player.progressMsF, player.batch, player.blurGlider.GetValue(), bgAlpha, player.bgCamera.GetProjectionView())

	if player.progressMsF > 0 {
		timeDiff := player.progressMsF - player.lastProgressMsF
		settings.Cursor.Colors.Update(timeDiff)
		player.lastProgressMsF = player.progressMsF
	}

	cursorColors := settings.Cursor.GetColors(settings.DIVIDES, len(player.controller.GetCursors()), player.Scl, player.cursorGlider.GetValue())

	if player.overlay != nil {
		player.drawOverlayPart(player.overlay.DrawBackground, cursorColors, cursorCameras[0], 1)
	}

	player.drawEpilepsyWarning()

	player.counter += timMs

	if player.counter >= 1000.0/60 {
		player.counter -= 1000.0 / 60
		if player.background.GetStoryboard() != nil {
			player.storyboardDrawn = player.background.GetStoryboard().GetRenderedSprites()
		}
	}

	player.drawCoin()

	scale2 := player.Scl
	if !settings.Cursor.ScaleToTheBeat {
		scale2 = 1
	}

	bloomEnabled := settings.Playfield.Bloom.Enabled

	if bloomEnabled {
		player.bloomEffect.SetThreshold(settings.Playfield.Bloom.Threshold)
		player.bloomEffect.SetBlur(settings.Playfield.Bloom.Blur)

		bPower := settings.Playfield.Bloom.Power
		if settings.Playfield.Bloom.BloomToTheBeat {
			bPower += settings.Playfield.Bloom.BloomBeatAddition * (player.Scl - 1.0) / (settings.Audio.BeatScale * 0.4)
		}

		player.bloomEffect.SetPower(bPower)
		player.bloomEffect.Begin()
	}

	if player.overlay != nil {
		player.drawOverlayPart(player.overlay.DrawBeforeObjects, cursorColors, objectCameras[0], player.objectsAlphaFail.GetValue())
	}

	player.objectContainer.Draw(player.batch, player.mainCamera.GetProjectionView(), objectCameras, player.progressMsF, float32(player.Scl), float32(player.objectsAlpha.GetValue()*player.objectsAlphaFail.GetValue()))

	if player.overlay != nil {
		player.drawOverlayPart(player.overlay.DrawNormal, cursorColors, objectCameras[0], 1)
	}

	player.background.DrawOverlay(player.progressMsF, player.batch, bgAlpha, player.bgCamera.GetProjectionView())

	if player.overlay != nil && player.overlay.ShouldDrawHUDBeforeCursor() {
		player.drawOverlayPart(player.overlay.DrawHUD, cursorColors, player.uiCamera.GetProjectionView(), 1)
	}

	if settings.Playfield.DrawCursors {
		for _, g := range player.controller.GetCursors() {
			g.UpdateRenderer()
		}

		player.batch.SetAdditive(false)

		graphics.BeginCursorRender()

		for j := 0; j < settings.DIVIDES; j++ {
			player.batch.SetCamera(cursorCameras[j])

			for i, g := range player.controller.GetCursors() {
				if player.overlay != nil && player.overlay.IsBroken(g) {
					continue
				}

				baseIndex := j*len(player.controller.GetCursors()) + i

				ind := baseIndex - 1
				if ind < 0 {
					ind = settings.DIVIDES*len(player.controller.GetCursors()) - 1
				}

				col1 := cursorColors[baseIndex]
				col2 := cursorColors[ind]

				g.DrawM(scale2, player.batch, col1, col2)
			}
		}

		graphics.EndCursorRender()
	}

	player.batch.SetAdditive(false)

	if player.overlay != nil && !player.overlay.ShouldDrawHUDBeforeCursor() {
		player.drawOverlayPart(player.overlay.DrawHUD, cursorColors, player.uiCamera.GetProjectionView(), 1)
	}

	if bloomEnabled {
		player.bloomEffect.EndAndRender()
	}

	player.drawDebug()
}

func (player *Player) drawEpilepsyWarning() {
	if player.epiGlider.GetValue() < 0.01 {
		return
	}

	player.batch.Begin()
	player.batch.ResetTransform()
	player.batch.SetColor(1, 1, 1, player.epiGlider.GetValue())
	player.batch.SetCamera(mgl32.Ortho(float32(-settings.Graphics.GetWidthF()/2), float32(settings.Graphics.GetWidthF()/2), float32(settings.Graphics.GetHeightF()/2), float32(-settings.Graphics.GetHeightF()/2), 1, -1))

	scl := scaling.Fit.Apply(player.Epi.Width, player.Epi.Height, float32(settings.Graphics.GetWidthF()), float32(settings.Graphics.GetHeightF()))
	scl = scl.Scl(0.5).Scl(0.66)
	player.batch.SetScale(scl.X64(), scl.Y64())
	player.batch.DrawUnit(*player.Epi)

	player.batch.ResetTransform()
	player.batch.End()
	player.batch.SetColor(1, 1, 1, 1)
}

func (player *Player) drawCoin() {
	if !settings.Playfield.Logo.Enabled || player.fxGlider.GetValue() < 0.01 {
		return
	}

	player.batch.Begin()
	player.batch.ResetTransform()
	player.batch.SetColor(1, 1, 1, player.fxGlider.GetValue())
	player.batch.SetCamera(mgl32.Ortho(float32(-settings.Graphics.GetWidthF()/2), float32(settings.Graphics.GetWidthF()/2), float32(settings.Graphics.GetHeightF()/2), float32(-settings.Graphics.GetHeightF()/2), 1, -1))

	player.coin.DrawVisualiser(settings.Playfield.Logo.DrawSpectrum)
	player.coin.Draw(player.progressMsF, player.batch)

	player.batch.ResetTransform()
	player.batch.End()
}

func (player *Player) drawOverlayPart(drawFunc func(*batch2.QuadBatch, []color2.Color, float64), cursorColors []color2.Color, camera mgl32.Mat4, alpha float64) {
	player.batch.Begin()
	player.batch.ResetTransform()
	player.batch.SetColor(1, 1, 1, 1)

	player.batch.SetCamera(camera)

	drawFunc(player.batch, cursorColors, player.hudGlider.GetValue()*alpha)

	player.batch.End()
	player.batch.ResetTransform()
	player.batch.SetColor(1, 1, 1, 1)
}

func (player *Player) drawDebug() {
	if settings.DEBUG || settings.Graphics.ShowFPS {
		padDown := 4.0
		size := 16.0

		drawShadowed := func(right bool, pos float64, text string) {
			pX := 0.0
			origin := vector.BottomLeft

			if right {
				pX = player.ScaledWidth
				origin = vector.BottomRight
			}

			pY := player.ScaledHeight - (size+padDown)*pos - padDown

			player.batch.SetColor(0, 0, 0, 1)
			player.font.DrawOrigin(player.batch, pX+size*0.1, pY+size*0.1, origin, size, true, text)

			player.batch.SetColor(1, 1, 1, 1)
			player.font.DrawOrigin(player.batch, pX, pY, origin, size, true, text)
		}

		player.batch.Begin()
		player.batch.ResetTransform()
		player.batch.SetColor(1, 1, 1, 1)
		player.batch.SetCamera(player.uiCamera.GetProjectionView())

		if settings.DEBUG {
			player.batch.SetColor(0, 0, 0, 1)
			player.font.DrawOrigin(player.batch, size*1.5*0.1, padDown+size*1.5*0.1, vector.TopLeft, size*1.5, false, player.mapFullName)

			player.batch.SetColor(1, 1, 1, 1)
			player.font.DrawOrigin(player.batch, 0, padDown, vector.TopLeft, size*1.5, false, player.mapFullName)

			type tx struct {
				pos  float64
				text string
			}

			var queue []tx

			drawWithBackground := func(pos float64, text string) {
				width := player.font.GetWidthMonospaced(size, text)
				player.batch.DrawStObject(vector.NewVec2d(0, (size+padDown)*pos), vector.CentreLeft, vector.NewVec2d(width, size+padDown), false, false, 0, color2.NewLA(0, 0.8), false, graphics.Pixel.GetRegion())

				queue = append(queue, tx{pos, text})
			}

			drawWithBackground(3, fmt.Sprintf("VSync: %t", settings.Graphics.VSync))
			drawWithBackground(4, fmt.Sprintf("Blur: %t", settings.Playfield.Background.Blur.Enabled))
			drawWithBackground(5, fmt.Sprintf("Bloom: %t", settings.Playfield.Bloom.Enabled))

			msaa := "OFF"
			if settings.Graphics.MSAA > 0 {
				msaa = strconv.Itoa(int(settings.Graphics.MSAA)) + "x"
			}

			drawWithBackground(6, fmt.Sprintf("MSAA: %s", msaa))

			drawWithBackground(7, fmt.Sprintf("FBO Binds: %d", statistic.GetPrevious(statistic.FBOBinds)))
			drawWithBackground(8, fmt.Sprintf("VAO Binds: %d", statistic.GetPrevious(statistic.VAOBinds)))
			drawWithBackground(9, fmt.Sprintf("VBO Binds: %d", statistic.GetPrevious(statistic.VBOBinds)))
			drawWithBackground(10, fmt.Sprintf("Vertex Upload: %.2fk", float64(statistic.GetPrevious(statistic.VertexUpload))/1000))
			drawWithBackground(11, fmt.Sprintf("Vertices Drawn: %.2fk", float64(statistic.GetPrevious(statistic.VerticesDrawn))/1000))
			drawWithBackground(12, fmt.Sprintf("Draw Calls: %d", statistic.GetPrevious(statistic.DrawCalls)))
			drawWithBackground(13, fmt.Sprintf("Sprites Drawn: %d", statistic.GetPrevious(statistic.SpritesDrawn)))

			if storyboard := player.background.GetStoryboard(); storyboard != nil {
				drawWithBackground(14, fmt.Sprintf("SB sprites: %d", player.storyboardDrawn))
			}

			player.batch.ResetTransform()

			for _, t := range queue {
				player.font.DrawOrigin(player.batch, 0, (size+padDown)*t.pos, vector.CentreLeft, size, true, t.text)
			}

			currentTime := int(player.musicPlayer.GetPosition())
			totalTime := int(player.musicPlayer.GetLength())
			mapTime := int(player.bMap.HitObjects[len(player.bMap.HitObjects)-1].GetEndTime() / 1000)

			drawShadowed(false, 2, fmt.Sprintf("%02d:%02d / %02d:%02d (%02d:%02d)", currentTime/60, currentTime%60, totalTime/60, totalTime%60, mapTime/60, mapTime%60))
			drawShadowed(false, 1, fmt.Sprintf("%d(*%d) hitobjects, %d total", player.objectContainer.GetNumProcessed(), settings.DIVIDES, len(player.bMap.HitObjects)))

			if storyboard := player.background.GetStoryboard(); storyboard != nil {
				drawShadowed(false, 0, fmt.Sprintf("%d storyboard sprites, %d in queue (%d total)", player.background.GetStoryboard().GetProcessedSprites(), storyboard.GetQueueSprites(), storyboard.GetTotalSprites()))
			} else {
				drawShadowed(false, 0, "No storyboard")
			}
		}

		if settings.DEBUG || settings.Graphics.ShowFPS {
			fpsC := player.profiler.GetFPS()
			fpsU := player.profilerU.GetFPS()

			sbThread := player.background.GetStoryboard() != nil && player.background.GetStoryboard().HasVisuals()

			drawFPS := fmt.Sprintf("%0.0ffps (%0.2fms)", fpsC, 1000/fpsC)
			updateFPS := fmt.Sprintf("%0.0ffps (%0.2fms)", fpsU, 1000/fpsU)
			sbFPS := ""

			off := 0.0

			if sbThread {
				off = 1.0

				fpsS := player.background.GetStoryboard().GetFPS()
				sbFPS = fmt.Sprintf("%0.0ffps (%0.2fms)", fpsS, 1000/fpsS)
			}

			shift := strconv.Itoa(mutils.Max(len(drawFPS), mutils.Max(len(updateFPS), len(sbFPS))))

			drawShadowed(true, 1+off, fmt.Sprintf("Draw: %"+shift+"s", drawFPS))
			drawShadowed(true, 0+off, fmt.Sprintf("Update: %"+shift+"s", updateFPS))

			if sbThread {
				drawShadowed(true, 0, fmt.Sprintf("Storyboard: %"+shift+"s", sbFPS))
			}
		}

		player.batch.End()
	}
}

func (player *Player) Show() {}

func (player *Player) Hide() {}

func (player *Player) Dispose() {}
