package app.pinky.companion

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * The person's model keys, one per service under its key name
 * (TaskSettings.keyName: the service's ID, or the API's for the custom
 * entries and for keys saved before there were services), kept encrypted with an AES key
 * that lives in the Android Keystore and never leaves it. Only the
 * ciphertext is in this app's files, and neither backups nor transfers to
 * a new phone take it (allowBackup="false", data_extraction_rules.xml):
 * nowhere else could it be read. Nothing here logs a key or says how long
 * it is.
 */
class Secrets(context: Context) {

    private val prefs = context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** Whether a key is saved for this key name. */
    fun has(keyName: String): Boolean = prefs.contains(name(keyName))

    /**
     * The saved key for this key name, or null when there is none or it can
     * no longer be read (the Keystore key was lost, for example after the
     * phone's screen lock was removed on some phones): the person types it
     * again.
     */
    fun load(keyName: String): String? {
        val stored = prefs.getString(name(keyName), null) ?: return null
        return try {
            val raw = Base64.decode(stored, Base64.NO_WRAP)
            if (raw.size <= IV_BYTES) return null
            val c = Cipher.getInstance(TRANSFORMATION)
            c.init(Cipher.DECRYPT_MODE, secretKey(), GCMParameterSpec(TAG_BITS, raw, 0, IV_BYTES))
            String(c.doFinal(raw, IV_BYTES, raw.size - IV_BYTES), Charsets.UTF_8)
        } catch (_: Exception) {
            null
        }
    }

    /** Saves the key for this key name, replacing any saved before. */
    fun save(keyName: String, key: String) {
        val c = Cipher.getInstance(TRANSFORMATION)
        c.init(Cipher.ENCRYPT_MODE, secretKey())
        val iv = c.iv
        val sealed = c.doFinal(key.toByteArray(Charsets.UTF_8))
        val out = ByteArray(iv.size + sealed.size)
        System.arraycopy(iv, 0, out, 0, iv.size)
        System.arraycopy(sealed, 0, out, iv.size, sealed.size)
        prefs.edit().putString(name(keyName), Base64.encodeToString(out, Base64.NO_WRAP)).apply()
    }

    /** Forgets the key for this key name. */
    fun forget(keyName: String) {
        prefs.edit().remove(name(keyName)).apply()
    }

    private fun name(keyName: String) = "key_$keyName"

    private fun secretKey(): SecretKey {
        val ks = KeyStore.getInstance(KEYSTORE).apply { load(null) }
        (ks.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, KEYSTORE)
        gen.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return gen.generateKey()
    }

    companion object {
        private const val PREFS = "secrets"
        private const val KEYSTORE = "AndroidKeyStore"
        private const val ALIAS = "pinky_model_keys"
        private const val TRANSFORMATION = "AES/GCM/NoPadding"
        private const val IV_BYTES = 12
        private const val TAG_BITS = 128
    }
}
