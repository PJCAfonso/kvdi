// Package audio contains utilities for streaming audio from a desktop to
// a websocket client. It is used by the kvdi-proxy to provide an audio stream
// to the web UI.
package audio

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"

	"github.com/tinyzimmer/kvdi/pkg/util/audio/gst"
	"github.com/tinyzimmer/kvdi/pkg/util/audio/pa"
)

// Codec represents the encoder to use to process the raw PCM data.
type Codec string

const (
	// CodecOpus encodes the audio with opus and wraps it in a webm container.
	CodecOpus Codec = "opus"
	// CodecVorbis encodes the audio with vorbis and wraps it in an ogg container.
	CodecVorbis Codec = "vorbis"
	// CodecMP3 encodes the audio with lame and returns it in MP3 format.
	CodecMP3 Codec = "mp3"
	// CodecRaw uses raw PCM data with the configured sample rate
	CodecRaw Codec = "raw"
)

// Buffer provides a Reader interface for proxying audio data to a websocket
// connection
type Buffer struct {
	logger                                logr.Logger
	deviceManager                         *pa.DeviceManager
	pbkPipeline, bufPipeline, recPipeline *gst.Pipeline
	channels, sampleRate                  int
	userID, nullSinkID, inputDeviceID     string
	closed                                bool
}

var _ io.ReadCloser = &Buffer{}

// NewBuffer returns a new Buffer.
func NewBuffer(logger logr.Logger, userID string) *Buffer {
	return &Buffer{
		deviceManager: pa.NewDeviceManager(logger.WithName("pa_devices"), userID),
		userID:        userID,
		logger:        logger.WithName("audio_buffer"),
		channels:      2,
		sampleRate:    24000,
	}
}

// buildPlaybackPipeline builds a GST pipeline for recording data from the dummy monitor
// and making it available on the io.Reader interface.
func (a *Buffer) buildPlaybackPipeline(codec Codec) *gst.Pipeline {
	pipeline := gst.NewPipeline(a.userID, a.logger.WithName("gst_playback")).
		WithPulseSrc(a.userID, "kvdi.monitor", a.channels, a.sampleRate)

	switch codec {
	case CodecVorbis:
		pipeline = pipeline.WithVorbisEncode().WithOggMux()
	case CodecOpus:
		pipeline = pipeline.WithCutter().WithOpusEncode().WithWebmMux()
	case CodecMP3:
		pipeline = pipeline.WithLameEncode()
	default:
		a.logger.Info(fmt.Sprintf("Invalid codec for gst pipeline %s, defaulting to opus/webm", codec))
		pipeline = pipeline.WithCutter().WithOpusEncode().WithWebmMux()
	}

	return pipeline.WithFdSink(1)
}

// buildRecordingPipelines builds GST pipelines for receiving data from the Write interface
// and writing it to the source on the pipeline.
func (a *Buffer) buildRecordingPipelines(codec Codec) (*gst.Pipeline, *gst.Pipeline) {
	bufPipeline := gst.NewPipeline(a.userID, a.logger.WithName("gst_buffer")).
		WithTCPSrc("127.0.0.1", 9004, true).
		WithCaps("application/x-rtp", nil).
		WithPlugin("rtpjitterbuffer latency=20 do-lost=True").
		WithPlugin("rtpopusdepay").
		WithOpusDecode(true).
		// WithAudioConvert().
		// WithAudioResample().
		WithFileSink("/mnt/home/test.raw", true)
		// WithFdSink(1)
	recPipeline := gst.NewPipeline(a.userID, a.logger.WithName("gst_recorder")).
		WithFdSrc(0).
		WithOggDemux().
		WithPlugin("rtpopuspay").
		WithPlugin("tcpclientsink host=127.0.0.1 port=9004")
	return bufPipeline, recPipeline
}

func (a *Buffer) setupDevices() error {
	if err := a.deviceManager.AddSink("kvdi", "kvdi-playback"); err != nil {
		return err
	}

	if err := a.deviceManager.AddSource(
		"virtmic",
		"kvdi-microphone",
		fmt.Sprintf("/run/user/%s/pulse/mic.fifo", a.userID),
		"s16le", 1, 16000,
	); err != nil {
		return err
	}

	return a.deviceManager.SetDefaultSource("virtmic")
}

// SetChannels sets the number of channels to record from gstreamer. When this method is not called
// the value defaults to 2 (stereo).
func (a *Buffer) SetChannels(c int) { a.channels = c }

// SetSampleRate sets the sample rate to use when recording from gstreamer. When this method is not called
// the value defaults to 24000.
func (a *Buffer) SetSampleRate(r int) { a.sampleRate = r }

func (a *Buffer) waitForProcPort(proto string, pid, port, retries, interval int64) error {
	portHex := strings.ToUpper(strconv.FormatInt(port, 16))
	tries := int64(0)
	ticker := time.NewTicker(time.Second * time.Duration(interval))
	for range ticker.C {
		f, err := os.Open(fmt.Sprintf("/proc/%d/net/%s", pid, proto))
		if err != nil {
			return err
		}
		body, err := ioutil.ReadAll(f)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if strings.Contains(string(body), portHex) {
			break
		}
		if tries == retries {
			return fmt.Errorf("Hit retry limit waiting for port %s/%d on PID %d", proto, port, pid)
		}
	}
	return nil
}

// Start starts the gstreamer processes
func (a *Buffer) Start(codec Codec) error {
	if err := a.setupDevices(); err != nil {
		return err
	}

	a.pbkPipeline = a.buildPlaybackPipeline(codec)
	a.bufPipeline, a.recPipeline = a.buildRecordingPipelines(codec)

	// Block while starting the playback device
	if err := a.pbkPipeline.Start(); err != nil {
		return err
	}

	// Block while starting the buffer pipeline
	if err := a.bufPipeline.Start(); err != nil {
		return err
	}

	// Spawn a goroutine copying the buffer pipeline to the virtmic fifo.
	// TODO: Handle errors here.
	go func() {
		// Wait for the buffer pipeline to be ready to receive connections
		if err := a.waitForProcPort("tcp", int64(a.bufPipeline.Pid()), 9004, 5, 1); err != nil {
			a.logger.Error(err, "Buffer port did not become available")
			return
		}
		if err := a.recPipeline.Start(); err != nil {
			a.logger.Error(err, "Failed to start recorder pipeline.")
			return
		}
		// f, err := fifo.OpenFifo(context.Background(), fmt.Sprintf("/run/user/%s/pulse/mic.fifo", a.userID), syscall.O_WRONLY, 0644)
		// if err != nil {
		// 	a.logger.Error(err, "Failed to open virtmic fifo")
		// 	return
		// }
		// if _, err := io.Copy(f, a.bufPipeline); err != nil {
		// 	a.logger.Error(err, "Error while copying buffer pipeline to virtmic fifo")
		// }
	}()

	return nil
}

// Wait will join to the streaming process and block until its finished,
// returning an error if the process exits non-zero.
func (a *Buffer) Wait() error {
	errs := make([]string, 0)
	if err := a.pbkPipeline.Wait(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := a.bufPipeline.Wait(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := a.recPipeline.Wait(); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, " : "))
	}
	return nil
}

// Errors returns any errors that ocurred during the streaming process.
func (a *Buffer) Errors() []error {
	errs := make([]error, 0)
	if err := a.pbkPipeline.Error(); err != nil {
		errs = append(errs, err)
	}
	if err := a.bufPipeline.Error(); err != nil {
		errs = append(errs, err)
	}
	if err := a.recPipeline.Error(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// Stderr returns any output from stderr on the streaming process.
func (a *Buffer) Stderr() string {
	return strings.Join([]string{a.pbkPipeline.Stderr(), a.bufPipeline.Stderr(), a.recPipeline.Stderr()}, " : ")
}

// Read implements ReadCloser and returns data from the audio buffer.
func (a *Buffer) Read(p []byte) (int, error) { return a.pbkPipeline.Read(p) }

// Write implements a WriteCloser and writes data to the audio buffer.
func (a *Buffer) Write(p []byte) (int, error) { return a.recPipeline.Write(p) }

// IsClosed returns true if the buffer is closed.
func (a *Buffer) IsClosed() bool {
	return a.pbkPipeline.IsClosed() && a.recPipeline.IsClosed() && a.bufPipeline.IsClosed() && a.closed
}

// Close kills the gstreamer processes and unloads pa modules.
func (a *Buffer) Close() error {
	if !a.IsClosed() {
		if err := a.pbkPipeline.Close(); err != nil {
			return err
		}
		if err := a.bufPipeline.Close(); err != nil {
			return err
		}
		if err := a.recPipeline.Close(); err != nil {
			return err
		}
		a.deviceManager.Destroy()
		a.closed = true
	}
	return nil
}
