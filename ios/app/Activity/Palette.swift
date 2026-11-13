import SwiftUI
import UIKit

/// Desktop Pinky's palette, which the Android app's Look has too: the
/// accent, green for done and running, amber for what waits for the
/// person, red only for Stop and errors (docs/notes/phone-apps-ux.md,
/// "Look").
extension Color {
    static let pinkyAccent = Color(light: 0x5450D6, dark: 0x9895FF)
    static let pinkyGreen = Color(light: 0x1F9D55, dark: 0x4ADE80)
    static let pinkyAmber = Color(light: 0xB45309, dark: 0xFBBF24)

    /// A color for each appearance, from 0xRRGGBB.
    init(light: UInt32, dark: UInt32) {
        self.init(uiColor: UIColor { t in
            let v = t.userInterfaceStyle == .dark ? dark : light
            return UIColor(red: CGFloat(v >> 16 & 0xFF) / 255, green: CGFloat(v >> 8 & 0xFF) / 255,
                           blue: CGFloat(v & 0xFF) / 255, alpha: 1)
        })
    }
}
