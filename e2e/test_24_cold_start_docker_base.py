"""24: DROPPED — base_image.docker was removed in Ship 4 Task 1 schema delta.

The `base_image: {docker: true}` knob no longer exists; BaseImage is
an empty struct kept only for YAML compatibility. There is no DinD
template to select. The test pinned a behavior (docker CLI available
inside the VM) that is no longer a devm-controlled distinction.

See internal/schema/schema.go BaseImage comment.
"""
# No tests in this file — dropped intentionally.
