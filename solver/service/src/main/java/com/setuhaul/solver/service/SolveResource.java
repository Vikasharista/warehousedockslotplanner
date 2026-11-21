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

/** POST /solve plans a facility as of a time and stores the run; GET /runs/{id} returns a stored run. */
@Path("/")
@Produces(MediaType.APPLICATION_JSON)
public class SolveResource {

    @Inject
    PlanService plans;

    @Inject
    RunRepository runs;

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
