package app.pinky.companion

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

class ModelServicesTest {

    // The list desktop Pinky generates into the app (go generate ./internal/pilot).
    private val list = ModelServices.parse(File("src/main/assets/model_services.json").readText())

    private fun svc(id: String) = list.first { it.id == id }

    @Test
    fun readsTheGeneratedList() {
        assertEquals(TaskSettings.ANTHROPIC, list.first().id)
        assertEquals(list.size, list.map { it.id }.toSet().size)
        for (s in list) {
            assertTrue(s.id, s.api == TaskSettings.ANTHROPIC || s.api == TaskSettings.OPENAI)
            assertEquals(s.id, if (s.custom) s.api else s.id, s.keyName)
        }
        val ds = svc("deepseek")
        assertEquals("https://api.deepseek.com", ds.baseUrl)
        assertEquals("deepseek-flash", ds.defaultModel)
        assertTrue(ds.priced("deepseek-flash"))
        assertFalse(ds.priced(""))
        assertFalse(svc("kimi-cn").priced("kimi-k3"))
        assertEquals(ModelServices.REGION_CHINA, svc("kimi-cn").region)

        val ollama = svc("ollama")
        assertTrue(ollama.keyOptional && ollama.local)
        assertEquals("", ollama.defaultModel)

        assertEquals(TaskSettings.DEFAULT_MODEL, svc(TaskSettings.ANTHROPIC).defaultModel)
        assertEquals("Claude Opus 5.5", svc(TaskSettings.ANTHROPIC).modelLabel("claude-opus-5-5"))
        assertEquals("my-model", svc(TaskSettings.ANTHROPIC).modelLabel("my-model"))
        // A Claude model at an address of the person's has Anthropic's prices.
        val ca = svc(ModelServices.CUSTOM_ANTHROPIC)
        assertEquals(TaskSettings.DEFAULT_MODEL, ca.defaultModel)
        assertTrue(ca.priced("claude-sonnet-5-5"))
        assertEquals(TaskSettings.ANTHROPIC, ca.keyName)
        val co = svc(ModelServices.CUSTOM_OPENAI)
        assertEquals("", co.defaultModel)
        assertEquals(TaskSettings.OPENAI, co.keyName)
    }

    @Test
    fun leavesOutWhatItDoesNotKnow() {
        val l = ModelServices.parse("""{"services":[{"id":"x","name":"X","api":"grpc"},{"id":"y","name":"Y","api":"openai"}]}""")
        assertEquals(listOf("y"), l.map { it.id })
        assertTrue(l[0].models.isEmpty() && l[0].priced.isEmpty() && !l[0].keyOptional)
    }

    @Test
    fun settingsOfBefore() {
        for ((case, want) in listOf(
            Triple(TaskSettings.ANTHROPIC, "", TaskSettings.ANTHROPIC),
            Triple(TaskSettings.ANTHROPIC, "https://proxy.example/", ModelServices.CUSTOM_ANTHROPIC),
            Triple(TaskSettings.OPENAI, "", TaskSettings.OPENAI),
            Triple(TaskSettings.OPENAI, "https://api.openai.com/v1/", TaskSettings.OPENAI),
            // An address typed by hand stays the person's, even a service's.
            Triple(TaskSettings.OPENAI, "https://api.deepseek.com", ModelServices.CUSTOM_OPENAI),
        ).map { (p, u, w) -> (p to u) to w }) {
            assertEquals(case.toString(), want, ModelServices.serviceFor(case.first, case.second))
        }

        // An OpenAI-compatible service with a model, as Settings kept it before.
        val old = mapOf("provider" to "openai", "base_url_openai" to "https://open.example/v1", "model_openai" to "glm-5",
            "model_anthropic" to "claude-sonnet-5-5")
        val get = { k: String -> old[k] }
        val all = list.map { it.id }.toSet()
        assertEquals(ModelServices.CUSTOM_OPENAI, TaskSettings.serviceIn(get) { it in all })
        assertEquals("glm-5", TaskSettings.modelIn(get, ModelServices.CUSTOM_OPENAI))
        assertEquals("https://open.example/v1", TaskSettings.urlIn(get, ModelServices.CUSTOM_OPENAI))
        // OpenAI's own service shares the model, not the address.
        assertEquals("glm-5", TaskSettings.modelIn(get, TaskSettings.OPENAI))
        assertEquals("", TaskSettings.urlIn(get, TaskSettings.OPENAI))
        assertEquals("claude-sonnet-5-5", TaskSettings.modelIn(get, TaskSettings.ANTHROPIC))
        assertEquals("", TaskSettings.modelIn(get, "deepseek"))
        // Nothing set at all: Anthropic.
        assertEquals(TaskSettings.ANTHROPIC, TaskSettings.serviceIn({ null }) { it in all })

        // Settings of now: per service, and an unknown service falls back.
        val now = old + mapOf("service" to "kimi-cn", "svc_model_kimi-cn" to "kimi-k3", "svc_url_openai-compatible" to "")
        val get2 = { k: String -> now[k] }
        assertEquals("kimi-cn", TaskSettings.serviceIn(get2) { it in all })
        assertEquals("kimi-k3", TaskSettings.modelIn(get2, "kimi-cn"))
        assertEquals("", TaskSettings.urlIn(get2, ModelServices.CUSTOM_OPENAI)) // set back to the default
        assertEquals(ModelServices.CUSTOM_OPENAI, TaskSettings.serviceIn({ k: String -> (now + ("service" to "gone"))[k] }) { it in all })
    }

    @Test
    fun usage() {
        val rows = listOf(
            ModelUsage.parse(JSONObject("""{"day":"2026-11-13","service":"deepseek","model":"deepseek-flash","requests":3,"input":40000,"output":1000,"cost":0.0123}""")),
            ModelUsage.parse(JSONObject("""{"day":"2026-11-12","service":"openai-compatible","host":"open.example","model":"m","requests":1,"input":10,"output":5,"cache_read":3,"cache_write":2,"unpriced":20}""")),
        )
        assertEquals(41_000L, rows[0].tokens)
        assertEquals(20L, rows[1].tokens)
        assertEquals("open.example", rows[1].host)
        val all = ModelUsage.total(rows)
        assertEquals(ModelUsage.Total(4, 41_020, 0.0123, 20), all)
        assertEquals(ModelUsage.Total(3, 41_000, 0.0123, 0), ModelUsage.total(rows, "2026-11-13"))
    }
}
