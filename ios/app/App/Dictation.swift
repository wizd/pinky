import AVFoundation
import Speech

/// Voice input for the task box, with Apple's speech recognition: on the
/// iPhone itself when its language allows (requiresOnDeviceRecognition),
/// otherwise through Apple's servers, as the keyboard's own dictation does.
/// What it hears goes into the box for the person to read and start; it
/// never starts a task by itself.
///
/// It listens only while the person asks it to (the mic button), at most a
/// minute, and only with no task running, so it never meets SilentAudio's
/// session, which plays during tasks in the lite build.
@MainActor
final class Dictation: ObservableObject {
    @Published private(set) var listening = false
    /// Why it could not listen, for the task box.
    @Published private(set) var problem: String?

    private var engine: AVAudioEngine?
    private var request: SFSpeechAudioBufferRecognitionRequest?
    private var task: SFSpeechRecognitionTask?
    private var timeout: Task<Void, Never>?

    /// Whether the phone has a recognizer for its language at all.
    static var available: Bool { SFSpeechRecognizer()?.isAvailable == true }

    /// Starts listening; `heard` gets the text heard so far, each time it
    /// grows, until stop() or the end of speech.
    func start(_ heard: @escaping (String) -> Void) async {
        problem = nil
        guard !listening else { return }
        if request != nil { finish() } // the last one's final result is not needed now
        guard await Self.allowed() else {
            problem = L.voiceNotAllowed
            return
        }
        guard let recognizer = SFSpeechRecognizer(), recognizer.isAvailable else {
            problem = L.voiceUnavailable
            return
        }
        let session = AVAudioSession.sharedInstance()
        do {
            try session.setCategory(.record, mode: .measurement, options: .duckOthers)
            try session.setActive(true, options: .notifyOthersOnDeactivation)
        } catch {
            problem = L.voiceUnavailable
            return
        }
        let request = SFSpeechAudioBufferRecognitionRequest()
        request.shouldReportPartialResults = true
        request.requiresOnDeviceRecognition = recognizer.supportsOnDeviceRecognition
        request.addsPunctuation = true
        let engine = AVAudioEngine()
        let input = engine.inputNode
        let format = input.outputFormat(forBus: 0)
        guard format.sampleRate > 0 else {
            // No microphone (the Simulator without one, for one).
            finish()
            problem = L.voiceUnavailable
            return
        }
        input.installTap(onBus: 0, bufferSize: 1024, format: format) { buffer, _ in
            request.append(buffer)
        }
        do {
            engine.prepare()
            try engine.start()
        } catch {
            input.removeTap(onBus: 0)
            finish()
            problem = L.voiceUnavailable
            return
        }
        self.engine = engine
        self.request = request
        listening = true
        task = recognizer.recognitionTask(with: request) { [weak self] result, error in
            let text = result?.bestTranscription.formattedString
            let done = error != nil || result?.isFinal == true
            Task { @MainActor in
                guard let self, self.request === request else { return }
                if let text, !text.isEmpty { heard(text) }
                if done { self.finish() }
            }
        }
        // Apple's recognizer takes about a minute of speech at a time.
        timeout = Task { [weak self] in
            try? await Task.sleep(for: .seconds(55))
            if !Task.isCancelled { self?.stop() }
        }
    }

    /// Stops listening; the recognizer gives what it heard last.
    func stop() {
        guard listening else { return }
        request?.endAudio()
        engine?.stop()
        engine?.inputNode.removeTap(onBus: 0)
        engine = nil
        listening = false
        timeout?.cancel()
        // The final result still comes, and finish() runs then; in case it
        // does not, the recognizer is let go a little later.
        let request = request
        timeout = Task { [weak self] in
            try? await Task.sleep(for: .seconds(3))
            guard let self, !Task.isCancelled, self.request === request else { return }
            self.finish()
        }
    }

    private func finish() {
        engine?.stop()
        engine?.inputNode.removeTap(onBus: 0)
        engine = nil
        task?.cancel()
        task = nil
        request = nil
        listening = false
        timeout?.cancel()
        timeout = nil
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    /// Asks for speech recognition and the microphone, the first time.
    private static func allowed() async -> Bool {
        let speech = await withCheckedContinuation { c in
            SFSpeechRecognizer.requestAuthorization { c.resume(returning: $0) }
        }
        guard speech == .authorized else { return false }
        return await AVAudioApplication.requestRecordPermission()
    }
}
