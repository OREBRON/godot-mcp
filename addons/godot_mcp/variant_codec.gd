@tool
extends RefCounted
## Converts between JSON values and Godot Variants for node properties.
## Values go out in a form that comes back in unchanged: plain JSON for
## numbers, strings and booleans, res:// paths for saved resources and Godot's
## text form ("Vector2(1, 2)") for everything else.

const MAX_TEXT := 4000


static func encode(v: Variant) -> Variant:
	match typeof(v):
		TYPE_NIL, TYPE_BOOL, TYPE_INT, TYPE_FLOAT, TYPE_STRING:
			return v
		TYPE_STRING_NAME, TYPE_NODE_PATH:
			return str(v)
		TYPE_OBJECT:
			if not is_instance_valid(v):
				return null
			if v is Resource:
				var path: String = v.resource_path
				if not path.is_empty() and not path.contains("::"):
					return path
				# Embedded sub-resource: read its fields with "property:field".
				return "<%s (embedded)>" % v.get_class()
			if v is Node:
				return "<Node %s>" % v.name
			return "<%s>" % v.get_class()
		TYPE_ARRAY:
			var out := []
			for e in v:
				out.append(encode(e))
			return out
		TYPE_DICTIONARY:
			var out := {}
			for k in v:
				out[str(k)] = encode(v[k])
			return out
	var text := var_to_str(v)
	if text.length() > MAX_TEXT:
		return text.substr(0, MAX_TEXT) + "... (%d characters, truncated)" % text.length()
	return text


## Converts a JSON value for a property of the given Variant type. Returns
## {"value": v} or {"error": message}. current is the property's present
## value, used for examples in error messages.
static func decode(value: Variant, type: int, current: Variant) -> Dictionary:
	if value == null:
		return {"value": null}
	match type:
		TYPE_NIL:
			return {"value": value}
		TYPE_BOOL:
			if value is bool:
				return {"value": value}
			if value is String and value.to_lower() in ["true", "false"]:
				return {"value": value.to_lower() == "true"}
		TYPE_INT:
			if value is float or value is int:
				return {"value": int(value)}
			if value is String and value.is_valid_int():
				return {"value": value.to_int()}
		TYPE_FLOAT:
			if value is float or value is int:
				return {"value": float(value)}
			if value is String and value.is_valid_float():
				return {"value": value.to_float()}
		TYPE_STRING:
			return {"value": str(value)}
		TYPE_STRING_NAME:
			return {"value": StringName(str(value))}
		TYPE_NODE_PATH:
			return {"value": NodePath(str(value))}
		TYPE_OBJECT:
			if value is String:
				if value.is_empty():
					return {"value": null}
				if value.begins_with("res://") or value.begins_with("uid://"):
					if not ResourceLoader.exists(value):
						return {"error": "%s does not exist" % value}
					var res := load(value)
					if res == null:
						return {"error": "%s could not be loaded; check it with script_check if it is a script" % value}
					return {"value": res}
			return {"error": "expected a res:// path to a resource or script, or null"}
		TYPE_COLOR:
			var c = _color(value)
			if c != null:
				return {"value": c}
		TYPE_VECTOR2, TYPE_VECTOR2I, TYPE_VECTOR3, TYPE_VECTOR3I, TYPE_VECTOR4, TYPE_VECTOR4I:
			var vec = _vector(value, type)
			if vec != null:
				return {"value": vec}
		TYPE_ARRAY:
			if value is Array:
				if current is Array and current.is_typed():
					return {"value": Array(value, current.get_typed_builtin(), current.get_typed_class_name(), current.get_typed_script())}
				return {"value": value}
		TYPE_DICTIONARY:
			if value is Dictionary:
				return {"value": value}
	# Anything else: Godot's text form, e.g. "Rect2(0, 0, 16, 16)".
	if value is String:
		var parsed = str_to_var(value)
		if typeof(parsed) == type:
			return {"value": parsed}
	if type >= TYPE_PACKED_BYTE_ARRAY and value is Array:
		var converted = type_convert(value, type)
		if typeof(converted) == type:
			return {"value": converted}
	var example: String = var_to_str(current) if current != null else type_string(type) + "(...)"
	return {"error": "expected %s, e.g. %s; got %s" % [type_string(type), example, JSON.stringify(value)]}


static func _color(value: Variant) -> Variant:
	if value is String:
		if value.begins_with("Color("):
			var c = str_to_var(value)
			return c if c is Color else null
		if Color.html_is_valid(value):
			return Color.html(value)
		var sentinel := Color(0.123, 0.456, 0.789, 0.321)
		var named := Color.from_string(value, sentinel)
		return null if named == sentinel else named
	if value is Array and value.size() in [3, 4]:
		return Color(value[0], value[1], value[2], value[3] if value.size() == 4 else 1.0)
	if value is Dictionary and value.has_all(["r", "g", "b"]):
		return Color(value.r, value.g, value.b, value.get("a", 1.0))
	return null


static func _vector(value: Variant, type: int) -> Variant:
	var n := 2
	if type in [TYPE_VECTOR3, TYPE_VECTOR3I]:
		n = 3
	elif type in [TYPE_VECTOR4, TYPE_VECTOR4I]:
		n = 4
	var c := []
	if value is Array and value.size() == n:
		c = value
	elif value is Dictionary and value.has_all(["x", "y", "z", "w"].slice(0, n)):
		for k in ["x", "y", "z", "w"].slice(0, n):
			c.append(value[k])
	else:
		return null
	for e in c:
		if not (e is float or e is int):
			return null
	match type:
		TYPE_VECTOR2:
			return Vector2(c[0], c[1])
		TYPE_VECTOR2I:
			return Vector2i(c[0], c[1])
		TYPE_VECTOR3:
			return Vector3(c[0], c[1], c[2])
		TYPE_VECTOR3I:
			return Vector3i(c[0], c[1], c[2])
		TYPE_VECTOR4:
			return Vector4(c[0], c[1], c[2], c[3])
		TYPE_VECTOR4I:
			return Vector4i(c[0], c[1], c[2], c[3])
	return null
