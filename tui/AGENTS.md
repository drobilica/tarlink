# TUI engineering policy

## Charm ownership

* Use Bubble Tea for event/state/update lifecycle.
* Prefer Bubbles components for reusable interactive primitives.
* Use Lip Gloss for visual layout, borders, padding, width, alignment, composition, and placement.
* Keep TarLink code focused on application-specific behavior and content.

## Do not hand-roll existing primitives

Before implementing generic TUI behavior, check whether Bubble Tea, Bubbles, or Lip Gloss provides it. Do not manually implement tables, viewport behavior, progress bars, help/key legends, text input, borders, padding/alignment/centering, or ANSI-aware visual layout unless the libraries genuinely cannot satisfy the requirement. Custom behavior must remain the smallest TarLink-specific policy layer around existing primitives.

## One owner per geometry concern

Do not combine independent layout systems for one component: no manually drawn border inside a Lip Gloss border, manually padded cells around Bubbles table geometry, literal spaces for centering controls, fixed-width Unicode container borders, or `fmt.Sprintf` column widths where Bubbles owns the geometry. Width, padding, border, and alignment must each have one clear owner.

## Responsive layout

* Derive layout from actual terminal dimensions and prefer width-aware components.
* Use display width, not byte/string length.
* Long content must truncate or wrap within its allocated region rather than shift neighboring UI.
* Do not add arbitrary per-terminal-size fixes.

## Bubbles first

When Bubbles has a maintained component appropriate to the interaction, use it unless there is a concrete reason not to; theme the component rather than replacing it with custom rendering. Do not force Bubbles into responsibilities for which it has no component; Lip Gloss remains the layout/styling primitive.

## Tests

Geometry, alignment, clipping, centering, and resizing bugs require regression tests that verify actual geometry, not only string presence. Test representative terminal widths and resize sequences, using ANSI/display-width-aware assertions.

## Scope

Keep TUI code presentation-only. Business/install/update/uninstall behavior remains in `internal/app`. Do not add a TUI framework without explicit authorization.
