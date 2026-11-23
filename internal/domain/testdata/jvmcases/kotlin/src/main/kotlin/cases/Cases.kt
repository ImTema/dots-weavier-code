// Every syntax shape jvm_field.go propagates through, for TestTraceFieldGolden.
package cases

data class Rate(val customerId: String, val cents: Long)

class Box {
    var id: String = "init"
    val tags = mutableListOf<String>()
}

class Cases {
    var last: String = "none"
    private val seed = "s".trim()

    fun clean(raw: String?) = raw!!.trim()

    fun pick(id: String, n: Int): String {
        val chosen = if (n > 0) {
            "big-$id"
        } else {
            id
        }
        return when (n) {
            0 -> chosen
            else -> "${chosen}x"
        }
    }

    fun flow(raw: String?, n: Int): String {
        val id = clean(raw) ?: "none"
        val picked = pick(id = id, n = n)
        val any: Any = picked
        val cast = (any as String)
        val total = -n * 2 + n % 3
        val range = 0..total
        val parts = arrayOf(cast, seed)
        val head = parts[0]
        val (a, b) = Pair(head, total)
        var acc = ""
        for ((i, p) in listOf(a, b.toString()).withIndex()) {
            acc = acc + i
            acc = acc + p
        }
        val box = Box()
        box.id = acc
        box.tags.add(box.id)
        last = box.tags[0]
        val slots = arrayOf("")
        slots[0] = last
        val rate = Rate(slots[0], range.last.toLong())
        val mapped = listOf(rate).map { r -> r.customerId.uppercase() }
        val all = listOf(*mapped.toTypedArray())
        val text = "${all.first()}-${rate.cents}"
        if (text.isBlank()) return text
        return text.lowercase()
    }

    fun caller() = println(flow(" 42 ", 3))
}
