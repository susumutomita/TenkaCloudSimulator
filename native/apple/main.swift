// Optional on-device helper. No shell, network client, disk transcript or logs.
// Build separately from the standalone Go simulator; wire protocol version 1.
import Foundation
import FoundationModels

struct Turn: Decodable { let role: String; let text: String }
struct Request: Decodable {
    let version: Int
    let model: String
    let system: String
    let messages: [Turn]
    let maxTokens: Int
    let temperature: Double?
    let topP: Double?
    let topK: Int?
}
struct Result: Encodable {
    let text: String
    let stop: String
    let inputTokens: Int
    let outputTokens: Int
}
struct Reply: Encodable {
    let version = 1
    var error: String? = nil
    var result: Result? = nil
}

@main struct Helper {
    static func emit(_ reply: Reply) {
        // Encoding errors and oversized output become fixed errors, never text logs.
        let data = try? JSONEncoder().encode(reply)
        let bounded = data.flatMap { $0.count <= 1 << 20 ? $0 : nil }
        FileHandle.standardOutput.write(bounded ?? Data(#"{"version":1,"error":"outputLimit"}"#.utf8))
    }

    static func main() async {
        guard CommandLine.arguments.count == 2,
              ["--check", "--generate"].contains(CommandLine.arguments[1]) else {
            emit(Reply(error: "unsupported")); return
        }
        // macOS 27 exposes actual generated token usage. Earlier APIs cannot
        // reliably distinguish a completed turn from a token-limited response.
        guard #available(macOS 27.0, *) else {
            emit(Reply(error: "unsupportedOS")); return
        }
        await run(mode: CommandLine.arguments[1])
    }

    @available(macOS 27.0, *)
    static func unavailable() -> String? {
        switch SystemLanguageModel.default.availability {
        case .available: return nil
        case .unavailable(.deviceNotEligible): return "deviceNotEligible"
        case .unavailable(.appleIntelligenceNotEnabled): return "appleIntelligenceNotEnabled"
        case .unavailable(.modelNotReady): return "modelNotReady"
        case .unavailable: return "modelNotReady"
        }
    }

    @available(macOS 27.0, *)
    static func run(mode: String) async {
        if let reason = unavailable() { emit(Reply(error: reason)); return }
        if mode == "--check" { emit(Reply()); return }
        do {
            var data = Data()
            while let chunk = try FileHandle.standardInput.read(upToCount: min(65536, (2 << 20) + 1 - data.count)), !chunk.isEmpty {
                data.append(chunk)
                if data.count > 2 << 20 { emit(Reply(error: "unsupported")); return }
            }
            let request = try JSONDecoder().decode(Request.self, from: data)
            guard request.version == 1, request.model == "system",
                  (1...8192).contains(request.maxTokens),
                  !request.messages.isEmpty, request.messages.count % 2 == 1,
                  request.topP == nil || request.topK == nil else {
                emit(Reply(error: "unsupported")); return
            }
            if let value = request.temperature, !(0...1).contains(value) { emit(Reply(error: "unsupported")); return }
            if let value = request.topP, !(value > 0 && value <= 1) { emit(Reply(error: "unsupported")); return }
            if let value = request.topK, !(1...500).contains(value) { emit(Reply(error: "unsupported")); return }
            // Restore roles as transcript entries; do not flatten assistant
            // turns into a user prompt or regenerate their supplied contents.
            var entries: [Transcript.Entry] = []
            if !request.system.isEmpty {
                entries.append(.instructions(.init(segments: [.text(.init(content: request.system))], toolDefinitions: [])))
            }
            for (index, turn) in request.messages.enumerated() {
                guard turn.role == (index % 2 == 0 ? "user" : "assistant"), !turn.text.isEmpty else {
                    emit(Reply(error: "unsupported")); return
                }
                if index == request.messages.count - 1 { break }
                let segments: [Transcript.Segment] = [.text(.init(content: turn.text))]
                if turn.role == "user" {
                    entries.append(.prompt(.init(segments: segments)))
                } else {
                    entries.append(.response(.init(segments: segments)))
                }
            }
            let session = LanguageModelSession(model: SystemLanguageModel.default, tools: [], transcript: Transcript(entries: entries))
            var sampling: GenerationOptions.SamplingMode? = nil
            if let value = request.topP { sampling = .random(probabilityThreshold: value) }
            if let value = request.topK { sampling = .random(top: value) }
            let options = GenerationOptions(samplingMode: sampling, temperature: request.temperature, maximumResponseTokens: request.maxTokens)
            let response = try await session.respond(to: request.messages.last!.text, options: options)
            // Foundation Models has no public stop reason. Fail conservatively
            // at the cap, even if a natural end might coincide with that count.
            guard response.usage.output.totalTokenCount < request.maxTokens else {
                emit(Reply(error: "tokenLimit")); return
            }
            guard response.usage.output.reasoningTokenCount == 0 else { emit(Reply(error: "unsupported")); return }
            for entry in response.transcriptEntries {
                switch entry {
                case .prompt: break
                case .response(let value):
                    for segment in value.segments {
                        guard case .text = segment else { emit(Reply(error: "unsupported")); return }
                    }
                default: emit(Reply(error: "unsupported")); return
                }
            }
            emit(Reply(result: Result(text: response.content, stop: "end", inputTokens: response.usage.input.totalTokenCount, outputTokens: response.usage.output.totalTokenCount)))
        } catch let error as LanguageModelError {
            switch error {
            case .contextSizeExceeded: emit(Reply(error: "contextLimit"))
            case .rateLimited: emit(Reply(error: "busy"))
            case .timeout: emit(Reply(error: "timeout"))
            default: emit(Reply(error: "generationFailed"))
            }
        } catch {
            // Never serialize localizedDescription: framework/decoder errors
            // can quote client content. Every process holds one ephemeral call.
            emit(Reply(error: "generationFailed"))
        }
    }
}
