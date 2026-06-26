package net.hotelpos.opsview

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class UrlPolicyTest {
    private val base = "https://ops.hotelpos.net/dashboard/wall"

    @Test fun sameHostIsInApp() {
        assertTrue(UrlPolicy.isInApp(base, "https://ops.hotelpos.net/dashboard"))
        assertTrue(UrlPolicy.isInApp(base, "https://ops.hotelpos.net/surv/ws/x/wall"))
    }

    @Test fun differentHostIsExternal() {
        assertFalse(UrlPolicy.isInApp(base, "https://evil.com/x"))
        assertFalse(UrlPolicy.isInApp(base, "https://ops.hotelpos.net.evil.com/x"))
    }

    @Test fun unparseableOrSchemeLinksAreExternal() {
        assertFalse(UrlPolicy.isInApp(base, "mailto:a@b.com"))
        assertFalse(UrlPolicy.isInApp(base, "not a url"))
    }
}
