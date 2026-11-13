import AVFoundation

/// Plays silence so iOS keeps the app running in the background (the
/// "audio" background mode). One of the ways Q2 tries to keep an engine
/// hosted in the app alive while another app is in front.
final class SilentAudio {
    private var engine: AVAudioEngine?

    var isOn: Bool { engine != nil }

    func set(_ on: Bool) {
        if on == isOn { return }
        if !on {
            engine?.stop()
            engine = nil
            try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
            return
        }
        let session = AVAudioSession.sharedInstance()
        try? session.setCategory(.playback, options: [.mixWithOthers])
        try? session.setActive(true)
        let e = AVAudioEngine()
        let format = AVAudioFormat(standardFormatWithSampleRate: 44100, channels: 2)!
        // Zeros, not flagged as silence, so the output really plays.
        let silence = AVAudioSourceNode(format: format) { _, _, _, buffers in
            for b in UnsafeMutableAudioBufferListPointer(buffers) {
                if let p = b.mData { memset(p, 0, Int(b.mDataByteSize)) }
            }
            return noErr
        }
        e.attach(silence)
        e.connect(silence, to: e.mainMixerNode, format: format)
        do {
            try e.start()
            engine = e
        } catch {
            NSLog("pinky silent audio: %@", error.localizedDescription)
        }
    }
}
