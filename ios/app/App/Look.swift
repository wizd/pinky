import SwiftUI

extension L.Tone {
    var color: Color {
        switch self {
        case .done: .pinkyGreen
        case .stopped: .secondary
        case .failed: .pinkyAmber
        }
    }

    var symbol: String {
        switch self {
        case .done: "checkmark.circle.fill"
        case .stopped: "stop.circle.fill"
        case .failed: "exclamationmark.circle.fill"
        }
    }
}

/// One step of the setup: its number, or a tick once done; the next one
/// shows what it is for and its button.
struct SetupStep<Content: View>: View {
    let number: Int
    let title: LocalizedStringKey
    let done: Bool
    let now: Bool
    @ViewBuilder var content: () -> Content

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            ZStack {
                Circle().fill(done ? Color.pinkyGreen : now ? Color.pinkyAccent : Color.secondary.opacity(0.25))
                if done {
                    Image(systemName: "checkmark").font(.caption2.bold()).foregroundStyle(.white)
                } else {
                    Text(verbatim: "\(number)").font(.caption.bold()).foregroundStyle(now ? .white : .secondary)
                }
            }
            .frame(width: 22, height: 22)
            .alignmentGuide(.firstTextBaseline) { $0[VerticalAlignment.center] + 5 }
            VStack(alignment: .leading, spacing: 8) {
                Text(title)
                    .font(.body.weight(now ? .semibold : .regular))
                    .foregroundStyle(done || !now ? .secondary : .primary)
                if now { content() }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.vertical, 2)
    }
}

/// Pinky's mark: the home screen icon (Assets.xcassets, PinkyMark) with
/// the corners iOS gives it. Decorative: the title beside it says Pinky.
struct PinkyMark: View {
    var size: CGFloat

    var body: some View {
        Image("PinkyMark")
            .resizable()
            .interpolation(.high)
            .frame(width: size, height: size)
            .clipShape(RoundedRectangle(cornerRadius: size * 0.225, style: .continuous))
            .accessibilityHidden(true)
    }
}

/// The task screen's title, Pinky, with the mark before it, as on Android
/// (docs/notes/phone-apps-ux.md, "Look"). From iOS 26 the mark sits in the
/// large title; once the list scrolls, the inline title is the plain
/// navigationTitle. Before iOS 26 the toolbar has no place in the large
/// title, so the mark leads the bar above it.
struct TitleWithMark: ViewModifier {
    func body(content: Content) -> some View {
        if #available(iOS 26.0, *) {
            content.toolbar {
                ToolbarItem(placement: .largeTitle) {
                    HStack(spacing: 10) {
                        PinkyMark(size: 34)
                        Text(verbatim: "Pinky").font(.largeTitle.bold())
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityElement(children: .combine)
                    .accessibilityAddTraits(.isHeader)
                }
            }
        } else {
            content.toolbar {
                ToolbarItem(placement: .topBarLeading) { PinkyMark(size: 30) }
            }
        }
    }
}
