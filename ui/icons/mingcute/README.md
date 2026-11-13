# Bundled MingCute icons

Pinky embeds these icons (`ui/icons.go`) and draws them in the color of the text around them, so they follow the light and dark themes. They come from two sets of the same family, by Richard:

| Folder | Set | License |
| --- | --- | --- |
| here | The free [MingCute](https://github.com/Richard9394/MingCute) set, npm [`mingcute_icon`](https://www.npmjs.com/package/mingcute_icon) 2.9.72 | Apache License 2.0, see [LICENSE](LICENSE) |
| [`pro/`](pro) | MingCute Icons System Pro v1.60, core style | Used under the owner's purchased license. Not covered by the Apache license: do not reuse them outside Pinky. |

The Pro icons are the ones the free set lacks: `eye_off` (Settings › Privacy and an approval whose screenshot is withheld), `lock_open` (Unlock outside the Mac), `cable` (an offline phone last seen over USB) and `list_check_4` (Checkup).

Only the icons the app shows are here, and `TestBundledIconsAreUsed` keeps it so. To add one:

1. Take it from the free set when it has the icon (mingcute.com, or `svg/<category>/<name>.svg` in the npm package), else from the Pro pack's `svg/core/regular` (and `svg/core/filled` for its filled form).
2. Copy it unchanged, as `<name>_line.svg` (and `<name>_fill.svg`), here for a free icon or in `pro/` for a Pro one, and add a variable for it in `ui/icons.go` (`mingcute("pro/<name>_line")` for Pro).

Icons are `_line` at rest; a selected row or tab shows the `_fill` of the same icon.
