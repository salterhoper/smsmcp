package com.salterhoper.smsmcp.gateway

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ProtocolTest {
    @Test fun parsesLinkPrintedByServer() {
        val l = PairingLink.parse("smsmcp://pair?token=smp_abc-_1&url=http%3A%2F%2F192.168.1.10%3A8080")
        assertEquals(PairingLink("http://192.168.1.10:8080", "smp_abc-_1"), l)
    }

    @Test fun rejectsOtherLinks() {
        assertNull(PairingLink.parse("https://example.com/pair?token=x&url=http://a"))
        assertNull(PairingLink.parse("smsmcp://pair?token=x"))
        assertNull(PairingLink.parse("smsmcp://pair?token=x&url=ftp%3A%2F%2Fa"))
    }

    @Test fun wsUrlFromServerUrl() {
        assertEquals("ws://h:8080/v1/device/ws", wsUrl("http://h:8080/"))
        assertEquals("wss://h/v1/device/ws", wsUrl("https://h"))
    }

    @Test fun statesOnlyMoveForward() {
        assertTrue(JobState.canAdvance(JobState.ACCEPTED, JobState.SENDING))
        assertTrue(JobState.canAdvance(JobState.UNKNOWN, JobState.SENT))
        assertFalse(JobState.canAdvance(JobState.SENT, JobState.UNKNOWN))
        assertFalse(JobState.canAdvance(JobState.DELIVERED, JobState.FAILED))
    }

    @Test fun dedupIsStable() {
        val a = inboundDedup("+255711", 1000, "hi")
        assertEquals(a, inboundDedup("+255711", 1000, "hi"))
        assertNotEquals(a, inboundDedup("+255711", 1001, "hi"))
        assertEquals(64, a.length)
    }
}
