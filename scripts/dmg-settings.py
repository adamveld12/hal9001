# dmgbuild settings for the HAL9001 installer (see scripts/make-dmg.sh). Values come from the environment so the
# script stays a plain settings file. Geometry matches internal/menubar/dmgbg.go (points; icon positions are centres).
import os

app = os.environ["DMG_APP"]
background = os.environ["DMG_BACKGROUND"]  # multi-resolution TIFF (1x + @2x)
icon = os.environ["DMG_ICON"]

format = "UDZO"
filesystem = "HFS+"
files = [app]
symlinks = {"Applications": "/Applications"}
icon = icon  # volume icon
badge_icon = icon
background = background
show_status_bar = False
show_tab_view = False
show_toolbar = False
show_pathbar = False
show_sidebar = False
window_rect = ((200, 120), (640, 400))
default_view = "icon-view"
arrange_by = None
icon_size = 128
text_size = 13
icon_locations = {
    os.path.basename(app): (170, 190),
    "Applications": (470, 190),
}
