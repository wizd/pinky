# Recording size

Issue #125: a person's activity history took 14 GB in a month, almost all of it task recordings. They want the activity kept but not high-quality replays. This note measures what recordings cost, compares three ways to make them smaller, and explains the one Pinky uses: **Settings › Privacy › Recording size**, Smaller (the default) or Original. Measured on 2026-11-14 on an Apple M4 Pro, macOS 27.2.

## What a recording costs now

The recorder remuxes the phone's own stream into a fragmented MP4 without decoding it (gateway.md, "Screen recordings"). What a minute costs is what the phone's encoder spends on it.

- **iPhone 13 mini (iOS 27, HEVC at 1136×2464, up to 60 fps):** 20 recordings of real tasks in the owner's history, 175 minutes and 2027 MB in all: **11.6 MB a minute** (1.54 Mbit/s) on average. The median recording is 10.6 MB a minute; they range from 0.3 to 30.1 by how much the screen moved. Only the sizes in the history database were read.
- Others in the same history: an iPad Pro (HEVC 2400×1680) 6.0 MB a minute; an iPhone on iOS 26 (H.264 562×1218, encoded by Pinky from WDA's MJPEG) 2.5 MB a minute.
- **Mi 10 Pro (Android, scrcpy HEVC at 1080×2340, 90 Hz screen):** the history had no Android recording, so a test recorded Settings on a background display for 2 minutes, scrolling like a task does: a swipe, then 2 to 5 s of reading. **17.7 MB a minute** (2.36 Mbit/s), 1039 pictures.

At 11.6 to 17.7 MB a minute, an hour of tasks a day for 30 days is 21 to 32 GB, which matches the issue.

## Three ways to make them smaller

**(a) Encode the recording again, smaller.** Decode, scale down, keep fewer pictures a second, encode at a lower bit rate. Prototypes with FFmpeg's `h264_videotoolbox` on the Mi 10 Pro clip (variable frame rate, area scaling, no B-frames):

| Long side | Pictures a second at most | Bit rate | MB a minute |
|---|---|---|---|
| 1280 | 10 | 500 kbit/s | 2.33 |
| 1280 | 10 | 800 kbit/s | 2.58 |
| 1280 | 10 | 1200 kbit/s | 2.61 |
| 1280 | 15 | 800 kbit/s | 2.86 |
| 960 | 10 | 500 kbit/s | 1.86 |
| 1600 | 10 | 1000 kbit/s | 3.45 |

The bit rate is a ceiling. A settled screen costs far less, which is why 800 and 1200 kbit/s come out close. Side by side with the original, at 1280 px and 800 kbit/s the smallest text in Settings reads as well as in the original, on a settled screen and in the middle of a scroll. At 960 px it is still readable but softer. Ten pictures a second follows a scroll well enough to see where it went.

**(b) Ask the phone for a smaller stream while a task records.** The recorder shares the stream with the mirror the person watches, and with the screen reads:
- iOS 27's CoreDevice stream takes no bit rate or size from Pinky.
- On Android, scrcpy's could be lowered, but the person would watch a blurrier mirror during every task.
- The screen reads would get worse too. iOS reads text from frames of this video (`ocrvideo_device_test.go`), and Android screenshots and the Wi-Fi OCR path use the same pictures.

A second, smaller stream for the recorder alone would double the phone's encoding work while a task runs, and an iPhone gives only one. Rejected.

**(c) Drop the static stretches.** Phones already send no pictures while the screen is still: in the Mi 10 Pro clip, 36 of 120 seconds had none at all. Key frames were 0.2% of the bytes, and pictures under 2 KB came to 35 KB in all. There is nothing left to drop. The bytes are in the moving stretches, at the phone's full size and frame rate. Rejected: it saves nothing.

## What Pinky does

(a), after the task ends. The recorder is unchanged: it remuxes the phone's stream, a running task's replay plays the growing file, and the reads that vouch for what may be recorded (gateway.md) work as before. When the task's file is closed, a worker in the gateway encodes it again (`recording.Shrink` with `recording.Smaller`):

- **Size:** the long side at most 1280 px, never enlarged, in even numbers.
- **Frame rate:** at most 10 pictures a second. A picture that comes sooner waits, and only the newest waiting one is encoded, at the end of its interval: until a newer picture came, it was what the screen showed then. So the screen a step settled on is always in the recording, at the time it appeared.
- **Encoding:** H.264 Main at 800 kbit/s with no B-frames and a key frame at most every 100 pictures, through `internal/h264enc`. That is VideoToolbox on macOS, not real-time so it may spend more effort, and FFmpeg's OpenH264 or Media Foundation encoder on Windows. Decoding uses the mirror's decoder (`video.Decoder`, now with `ScaleNV12`). Its BT.709 fallback also covers the Mi 10 Pro's YCgCo stream.
- **Kept as recorded:** the times since the start, the turns (`driver.Packet.Turn`) and size changes from rotations. A new encoder starts with a key frame for each new size.
- **Replacing the file:** the new file is written next to the original as `<task>.mp4.part`. `history.ReplaceRecording` then renames it over the original and updates the row. It does this under the store's write lock, and only while the task still has that recording, so deleting or pruning a task during the encode never brings its file back.
- **When the original stays:**
  - the new file is larger, as for a short, still recording;
  - the rename fails, as on Windows while a replay holds the file open;
  - Pinky quits first. A `.part` left by a quit is removed the next time a recording is made smaller.
- **Load:** one recording at a time, with two decoding threads. The Mi 10 Pro's 2-minute clip took 9.5 s, so a task's recording is done seconds after the task.

### Results

| | Before | After (Smaller) |
|---|---|---|
| Mi 10 Pro, Settings scrolled for 2 minutes | 17.7 MB/min (HEVC 1080×2340, 1039 pictures) | **3.0 MB/min** (H.264 590×1280, 519 pictures), 5.8× smaller |
| iPhone 13 mini, the owner's real tasks | 11.6 MB/min on average | about 2.5 to 3 MB/min, estimated: the phone was locked for the test, so it was not recorded |

The iPhone estimate follows from the encoder: the same scaling and rate, with a ceiling of 800 kbit/s (6 MB a minute while the screen moves the whole time), and about what the Android clip gave while it moved.

### Why it is the default

Most people use the replay to see what the agent did and saw: which screen, which button, what text. At 1280 px every step's text stays readable at a sixth of the size. Original is one select away for someone who wants every frame.

On Linux, Pinky has no H.264 encoder (`internal/h264enc`), so recordings stay as recorded whatever the setting. The gateway logs that once.

## Left out

- **Existing recordings** stay as they are; only recordings that finish from now on are made smaller. Retention removes the old ones in time. A button to shrink the history could come later.
- **HEVC output** would be smaller still, but Windows' FFmpeg has no HEVC encoder Pinky can call, and H.264 plays everywhere a report's recording is opened.
