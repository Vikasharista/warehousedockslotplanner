package com.setuhaul.solver.service;

import jakarta.inject.Inject;
import jakarta.ws.rs.Consumes;
import jakarta.ws.rs.GET;
import jakarta.ws.rs.POST;
import jakarta.ws.rs.Path;
import jakarta.ws.rs.PathParam;
import jakarta.ws.rs.Produces;
import jakarta.ws.rs.core.MediaType;
import jakarta.ws.rs.core.Response;
import jakarta.ws.rs.ext.ExceptionMapper;
import jakarta.ws.rs.ext.Provider;

import java.net.URI;

/**
 * POST /solve plans a facility as of a time and stores the run without booking anything. POST /replan plans a
 * facility now and commits it. POST /runs/{id}/commit commits a stored run. GET /runs/{id}, GET
 * /facilities/{id}/runs, GET /facilities/{id}/triggers and GET /clock read state.
 */
@Path("/")
@Produces(MediaType.APPLICATION_JSON)
public class SolveResource {

    @Inject
    PlanService plans;

    @Inject
    RunRepository runs;

    @Inject
    CommitService commits;

    @Inject
    ReplanService replans;

    @Inject
    TriggerScanner triggers;

    @Inject
    javax.sql.DataSource dataSource;

    @POST
    @Path("replan")
    @Consumes(MediaType.APPLICATION_JSON)
    public Api.ReplanResult replan(Api.ReplanRequest request) throws Exception {
        return replans.replan(request);
    }

    @POST
    @Path("runs/{runId}/commit")
    public Api.CommitResult commit(@PathParam("runId") String runId) throws Exception {
        return commits.commit(runId);
    }

    @GET
    @Path("facilities/{facilityId}/runs")
    public java.util.List<Api.Run> recent(@PathParam("facilityId") String facilityId,
            @jakarta.ws.rs.QueryParam("limit") @jakarta.ws.rs.DefaultValue("20") int limit) throws Exception {
        if (limit < 1 || limit > 200) {
            throw new IllegalArgumentException("limit must be between 1 and 200");
        }
        return runs.recent(facilityId, limit);
    }

    /** The trigger that would fire now, if any; for operators checking why a re-plan did or did not run. */
    @GET
    @Path("facilities/{facilityId}/triggers")
    public java.util.Map<String, Object> due(@PathParam("facilityId") String facilityId) throws Exception {
        var d = triggers.decide(facilityId);
        return d.<java.util.Map<String, Object>>map(x -> java.util.Map.of("due", true, "trigger", x.trigger(),
                "detail", x.detail())).orElse(java.util.Map.of("due", false));
    }

    @GET
    @Path("clock")
    public java.util.Map<String, Object> clock() throws Exception {
        try (var c = dataSource.getConnection()) {
            return java.util.Map.of("now", CommitService.now(c));
        }
    }

    @POST
    @Path("solve")
    @Consumes(MediaType.APPLICATION_JSON)
    public Response solve(Api.SolveRequest request) throws Exception {
        Api.Run run = plans.solve(request);
        return Response.created(URI.create("/runs/" + run.runId())).entity(run).build();
    }

    @GET
    @Path("runs/{runId}")
    public Response run(@PathParam("runId") String runId) throws Exception {
        return runs.find(runId)
                .map(r -> Response.ok(r).build())
                .orElseGet(() -> Response.status(Response.Status.NOT_FOUND)
                        .entity(new Api.Error("no run " + runId)).build());
    }

    @Provider
    public static class NotFoundMapper implements ExceptionMapper<CommitService.NotFound> {
        @Override
        public Response toResponse(CommitService.NotFound e) {
            return Response.status(Response.Status.NOT_FOUND).type(MediaType.APPLICATION_JSON)
                    .entity(new Api.Error(e.getMessage())).build();
        }
    }

    @Provider
    public static class ConflictMapper implements ExceptionMapper<CommitService.Conflict> {
        @Override
        public Response toResponse(CommitService.Conflict e) {
            return Response.status(Response.Status.CONFLICT).type(MediaType.APPLICATION_JSON)
                    .entity(new Api.Error(e.getMessage())).build();
        }
    }

    /** Bad input (missing fields, unknown facility, out-of-range limits) is the caller's error: 400 with a message. */
    @Provider
    public static class BadRequest implements ExceptionMapper<IllegalArgumentException> {
        @Override
        public Response toResponse(IllegalArgumentException e) {
            return Response.status(Response.Status.BAD_REQUEST).type(MediaType.APPLICATION_JSON)
                    .entity(new Api.Error(e.getMessage())).build();
        }
    }
}
